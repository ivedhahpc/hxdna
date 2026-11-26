package provision

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ivedhahpc/hxdna"
)

// Param keys of a build request. Provision decides every value (image, size, network, keys);
// the worker only applies them and never reads the catalog or the inventory system.
const (
	ParamBuildID         = "build_id"
	ParamName            = "name"
	ParamImageRef        = "image_ref" // the platform's own image ID, resolved by Provision from its catalog mapping
	ParamVCPUs           = "vcpus"
	ParamMemoryMB        = "memory_mb"
	ParamDiskGB          = "disk_gb"
	ParamVLAN            = "vlan"
	ParamIPCIDR          = "ip_cidr" // 10.0.0.5/24, or "dhcp"
	ParamGateway         = "gateway"
	ParamNameserver      = "nameserver"
	ParamSSHKeys         = "ssh_keys" // public keys, one per line
	ParamHostname        = "hostname"
	ParamPlatformOptions = "platform_options" // JSON object of string values
	ParamInstanceID      = "instance_id"
)

// legacyParams are the names v1 workers accept for one release while control planes move over.
var legacyParams = map[string]string{
	"template_id": ParamImageRef,
	"cores":       ParamVCPUs,
	"vlan_tag":    ParamVLAN,
}

// BuildRequest is a build's values. Each step sends the ones it needs: clone gets build_id,
// name, image_ref; configure gets build_id, instance_id and the rest. Zero means "not sent".
type BuildRequest struct {
	BuildID         string            `json:"build_id"`
	InstanceID      string            `json:"instance_id,omitempty"`
	Name            string            `json:"name,omitempty"`
	ImageRef        string            `json:"image_ref,omitempty"`
	VCPUs           int               `json:"vcpus,omitempty"`
	MemoryMB        int               `json:"memory_mb,omitempty"`
	DiskGB          int               `json:"disk_gb,omitempty"`
	VLAN            int               `json:"vlan,omitempty"`
	IPCIDR          string            `json:"ip_cidr,omitempty"`
	Gateway         string            `json:"gateway,omitempty"`
	Nameserver      string            `json:"nameserver,omitempty"`
	SSHKeys         []string          `json:"ssh_keys,omitempty"`
	Hostname        string            `json:"hostname,omitempty"`
	PlatformOptions map[string]string `json:"platform_options,omitempty"`
}

// Params is the request as HxCommand params (the control plane's side).
func (r BuildRequest) Params() map[string]string {
	p := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			p[k] = v
		}
	}
	setInt := func(k string, v int) {
		if v > 0 {
			p[k] = strconv.Itoa(v)
		}
	}
	set(ParamBuildID, r.BuildID)
	set(ParamInstanceID, r.InstanceID)
	set(ParamName, r.Name)
	set(ParamImageRef, r.ImageRef)
	setInt(ParamVCPUs, r.VCPUs)
	setInt(ParamMemoryMB, r.MemoryMB)
	setInt(ParamDiskGB, r.DiskGB)
	setInt(ParamVLAN, r.VLAN)
	set(ParamIPCIDR, r.IPCIDR)
	set(ParamGateway, r.Gateway)
	set(ParamNameserver, r.Nameserver)
	set(ParamSSHKeys, strings.Join(r.SSHKeys, "\n"))
	set(ParamHostname, r.Hostname)
	if len(r.PlatformOptions) > 0 {
		b, _ := json.Marshal(r.PlatformOptions)
		p[ParamPlatformOptions] = string(b)
	}
	return p
}

// ParseBuildRequest reads a build request from HxCommand params (the worker's side), checking
// the format of every value sent. acceptedOptions are the platform_options keys this worker
// declared; any other is refused. Every error is hxdna.Terminal: the request itself is wrong,
// so repeating it won't help. Which fields a step requires is the step's own check.
func ParseBuildRequest(params map[string]string, acceptedOptions []string) (BuildRequest, error) {
	p := map[string]string{}
	for k, v := range params {
		p[k] = strings.TrimSpace(v)
	}
	for old, cur := range legacyParams {
		if p[cur] == "" && p[old] != "" {
			p[cur] = p[old]
		}
	}
	r := BuildRequest{
		BuildID: p[ParamBuildID], InstanceID: p[ParamInstanceID], Name: p[ParamName], ImageRef: p[ParamImageRef],
		IPCIDR: p[ParamIPCIDR], Gateway: p[ParamGateway], Nameserver: p[ParamNameserver], Hostname: p[ParamHostname],
	}
	bad := func(format string, a ...any) (BuildRequest, error) {
		return BuildRequest{}, hxdna.Terminal(fmt.Errorf(format, a...))
	}

	var err error
	for _, f := range []struct {
		key      string
		dst      *int
		min, max int
	}{
		{ParamVCPUs, &r.VCPUs, 1, 1024},
		{ParamMemoryMB, &r.MemoryMB, 1, 1 << 24},
		{ParamDiskGB, &r.DiskGB, 1, 1 << 20},
		{ParamVLAN, &r.VLAN, 1, 4094},
	} {
		if p[f.key] == "" {
			continue
		}
		if *f.dst, err = strconv.Atoi(p[f.key]); err != nil || *f.dst < f.min || *f.dst > f.max {
			return bad("%s must be a whole number from %d to %d, got %q", f.key, f.min, f.max, p[f.key])
		}
	}
	if r.Name != "" && !dnsName.MatchString(r.Name) {
		return bad("name %q must be a DNS name (letters, digits, hyphens; dots between labels)", r.Name)
	}
	if r.Hostname != "" && !dnsName.MatchString(r.Hostname) {
		return bad("hostname %q must be a DNS name", r.Hostname)
	}
	if r.IPCIDR != "" && !strings.EqualFold(r.IPCIDR, "dhcp") {
		if _, _, err := net.ParseCIDR(r.IPCIDR); err != nil {
			return bad("ip_cidr %q must look like 10.0.0.5/24, or be dhcp", r.IPCIDR)
		}
	}
	if r.Gateway != "" && (r.IPCIDR == "" || strings.EqualFold(r.IPCIDR, "dhcp")) {
		return bad("gateway needs a static ip_cidr (DHCP hands out its own gateway)")
	}
	for _, k := range []string{ParamGateway, ParamNameserver} {
		if p[k] != "" && net.ParseIP(p[k]) == nil {
			return bad("%s %q is not an IP address", k, p[k])
		}
	}
	if p[ParamSSHKeys] != "" {
		if r.SSHKeys, err = ParseSSHPublicKeys(p[ParamSSHKeys]); err != nil {
			return BuildRequest{}, hxdna.Terminal(err)
		}
	}
	if p[ParamPlatformOptions] != "" {
		if err := json.Unmarshal([]byte(p[ParamPlatformOptions]), &r.PlatformOptions); err != nil {
			return bad("platform_options must be a JSON object of strings: %v", err)
		}
		for k := range r.PlatformOptions {
			if !slices.Contains(acceptedOptions, k) {
				return bad("platform option %q isn't one this worker accepts (%s)", k, strings.Join(acceptedOptions, ", "))
			}
		}
	}
	return r, nil
}

// dnsName is a DNS name: letters, digits and hyphens, dots between labels.
var dnsName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

var sshKeyTypes = []string{
	"ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
	"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com",
}

// ParseSSHPublicKeys reads public keys, one per line (blank lines and # comments skipped), and
// refuses anything else — above all a private key, which must never be stored or sent.
func ParseSSHPublicKeys(text string) ([]string, error) {
	if strings.Contains(text, "PRIVATE KEY") {
		return nil, fmt.Errorf("ssh_keys holds a private key — only public keys (ssh-ed25519 …) may be given")
	}
	var keys []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 || !slices.Contains(sshKeyTypes, f[0]) {
			return nil, fmt.Errorf("ssh_keys line %q is not a public key (expected e.g. ssh-ed25519 AAAA…)", shorten(line))
		}
		// A public key's blob starts with its own type, length-prefixed: a well-formed but
		// unrelated base64 string is caught here, not when someone can't log in.
		blob, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil || blobType(blob) != f[0] {
			return nil, fmt.Errorf("ssh_keys line %q has a malformed key", shorten(line))
		}
		keys = append(keys, line)
	}
	return keys, nil
}

// blobType reads the key type an SSH public key blob starts with ("" if it can't).
func blobType(blob []byte) string {
	if len(blob) < 4 {
		return ""
	}
	n := int(binary.BigEndian.Uint32(blob[:4]))
	if n <= 0 || n > 64 || len(blob) < 4+n {
		return ""
	}
	return string(blob[4 : 4+n])
}

func shorten(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
