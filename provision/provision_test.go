package provision

import (
	"errors"
	"strings"
	"testing"

	"github.com/ivedhahpc/hxdna"
)

const edKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHXDNxd/XXSO3ng0jfs2FnlvIdKlto60K2Ws8PxnxbPe ivedha@foreman"

func TestParamsRoundTrip(t *testing.T) {
	in := BuildRequest{
		BuildID: "b1", InstanceID: "103", Name: "verticaldna-test-02", ImageRef: "9000",
		VCPUs: 2, MemoryMB: 8192, DiskGB: 40, VLAN: 200,
		IPCIDR: "10.100.200.21/24", Gateway: "10.100.200.1", Nameserver: "1.1.1.1",
		SSHKeys: []string{edKey}, PlatformOptions: map[string]string{"storage": "local-lvm"},
	}
	out, err := ParseBuildRequest(in.Params(), []string{"storage"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != in.Name || out.VLAN != 200 || out.VCPUs != 2 || len(out.SSHKeys) != 1 || out.PlatformOptions["storage"] != "local-lvm" {
		t.Errorf("round trip lost values: %+v", out)
	}
}

func TestLegacyParams(t *testing.T) {
	r, err := ParseBuildRequest(map[string]string{"template_id": "9000", "cores": "4", "vlan_tag": "201"}, nil)
	if err != nil || r.ImageRef != "9000" || r.VCPUs != 4 || r.VLAN != 201 {
		t.Errorf("legacy names: %+v %v", r, err)
	}
	r, _ = ParseBuildRequest(map[string]string{"template_id": "1", "image_ref": "9000"}, nil)
	if r.ImageRef != "9000" {
		t.Errorf("the current name must win over the legacy one: %q", r.ImageRef)
	}
}

func TestParseRefusesBadValuesAsTerminal(t *testing.T) {
	bad := []map[string]string{
		{"vlan": "4095"},
		{"vcpus": "0"},
		{"name": "bad_name"},
		{"ip_cidr": "10.0.0.5"},
		{"gateway": "10.0.0.1"}, // no ip_cidr
		{"ip_cidr": "10.0.0.5/24", "gateway": "nope"},
		{"ip_cidr": "dhcp", "gateway": "10.0.0.1"},
		{"ssh_keys": "-----BEGIN OPENSSH PRIVATE KEY-----\nabc"},
		{"ssh_keys": "not a key"},
		{"platform_options": `{"datastore":"x"}`}, // not accepted
		{"platform_options": `["x"]`},
	}
	for _, p := range bad {
		_, err := ParseBuildRequest(p, []string{"storage"})
		if err == nil {
			t.Errorf("%v: want an error", p)
			continue
		}
		if hxdna.FailureKindOf(err) != hxdna.FailureTerminal {
			t.Errorf("%v: want terminal, got %q", p, hxdna.FailureKindOf(err))
		}
	}
	if _, err := ParseBuildRequest(map[string]string{"ip_cidr": "dhcp"}, nil); err != nil {
		t.Errorf("dhcp should be accepted: %v", err)
	}
}

func TestParseSSHPublicKeys(t *testing.T) {
	keys, err := ParseSSHPublicKeys("# lab key\n" + edKey + "\n\n")
	if err != nil || len(keys) != 1 {
		t.Fatalf("got %v %v", keys, err)
	}
	if _, err := ParseSSHPublicKeys("ssh-ed25519 !!!notbase64"); err == nil {
		t.Error("malformed key accepted")
	}
	// Valid base64, but not an ed25519 key blob.
	if _, err := ParseSSHPublicKeys("ssh-ed25519 aGVsbG8gd29ybGQgdGhpcyBpcyBub3QgYSBrZXk="); err == nil {
		t.Error("base64 that isn't a key blob accepted")
	}
	// The type word must match the blob (an ed25519 blob declared as rsa).
	if _, err := ParseSSHPublicKeys(strings.Replace(edKey, "ssh-ed25519", "ssh-rsa", 1)); err == nil {
		t.Error("type/blob mismatch accepted")
	}
}

func TestManifestExtension(t *testing.T) {
	m := hxdna.Manifest{WorkerType: "proxmox-worker"}
	if c, err := BuildContractOf(m); c != nil || err != nil {
		t.Fatalf("no contract: got %v %v", c, err)
	}
	if err := SetBuildContract(&m, BuildContract{Optional: []string{AbilityResize, AbilityConsole}, PlatformOptions: []string{"storage"}}); err != nil {
		t.Fatal(err)
	}
	c, err := BuildContractOf(m)
	if err != nil || c.Version != BuildContractVersion || !c.Has(AbilityResize) || c.Has(AbilitySnapshot) {
		t.Fatalf("read back: %+v %v", c, err)
	}
	if err := SetBuildContract(&m, BuildContract{Optional: []string{"teleport"}}); err == nil {
		t.Error("unknown ability accepted")
	}

	missing := MissingCommands(m)
	if !strings.Contains(strings.Join(missing, ","), "resize_instance") || len(missing) != len(RequiredCommands)+1 {
		t.Errorf("missing: %v", missing)
	}
	for _, k := range append(RequiredCommands, "resize_instance") {
		m.Commands = append(m.Commands, hxdna.Command{Key: k})
	}
	if missing := MissingCommands(m); len(missing) != 0 {
		t.Errorf("all registered, still missing %v", missing)
	}
}

func TestBuildLabel(t *testing.T) {
	notes := "Built by Provision\n" + BuildLabel("8f2c-41aa") + "\nowner: verticaldna"
	if got := BuildIDFrom(notes); got != "8f2c-41aa" {
		t.Errorf("BuildIDFrom = %q", got)
	}
	if BuildIDFrom("no label here") != "" {
		t.Error("found a label that isn't there")
	}
}

func TestTerminalUnwraps(t *testing.T) {
	_, err := ParseBuildRequest(map[string]string{"vlan": "0"}, nil)
	var fe *hxdna.FailureError
	if !errors.As(err, &fe) {
		t.Fatal("want a FailureError")
	}
}
