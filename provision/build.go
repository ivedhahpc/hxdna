package provision

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ivedhahpc/hxdna"
)

// BuildContractVersion is the BuildContract version this package describes.
const BuildContractVersion = 1

// BuildExtension is the key BuildContract is stored under in hxdna.Manifest.Extensions.
const BuildExtension = "provision.build"

// Commands every BuildContract worker registers (required).
const (
	CmdCloneInstance     = "clone_instance"     // copy an image into a new VM, labelled with the build; returns a task
	CmdConfigureInstance = "configure_instance" // CPU, memory, disk, network, access — only on a VM this build made
	CmdStartInstance     = "start_instance"     // power on a VM this build made; returns a task
	CmdTaskStatus        = "task_status"        // running | ok | error for one platform task
	CmdInstanceStatus    = "instance_status"    // one instance's observed state (InstanceStatus)
	CmdPowerInstance     = "power_instance"     // start | stop | stop_hard | reset
	CmdDestroyInstance   = "destroy_instance"   // delete a stopped VM; name typed to confirm; never a template
	CmdListTemplates     = "list_templates"     // the images this platform can clone
)

// RequiredCommands are the commands a worker declaring BuildContract must register.
var RequiredCommands = []string{
	CmdCloneInstance, CmdConfigureInstance, CmdStartInstance, CmdTaskStatus,
	CmdInstanceStatus, CmdPowerInstance, CmdDestroyInstance, CmdListTemplates,
}

// Optional abilities a worker may declare in BuildContract.Optional, each with its command.
const (
	AbilityResize   = "resize"   // resize_instance
	AbilityReimage  = "reimage"  // reimage_instance (destroys the VM's data)
	AbilitySnapshot = "snapshot" // snapshot_instance
	AbilityConsole  = "console"  // a console link (InstanceStatus.ConsoleURL)
)

// abilityCommands are the commands optional abilities need; console needs none.
var abilityCommands = map[string]string{
	AbilityResize:   "resize_instance",
	AbilityReimage:  "reimage_instance",
	AbilitySnapshot: "snapshot_instance",
}

// BuildContract is the manifest section of a VM worker.
type BuildContract struct {
	Version int `json:"version"`
	// Optional lists the abilities beyond the required ones (Ability*).
	Optional []string `json:"optional,omitempty"`
	// PlatformOptions are the platform_options keys this worker accepts (e.g. "storage").
	// Anything else sent is refused. Provision fills them only from its catalog mapping.
	PlatformOptions []string `json:"platform_options,omitempty"`
}

// Has reports whether the worker declared an optional ability.
func (c BuildContract) Has(ability string) bool { return slices.Contains(c.Optional, ability) }

// SetBuildContract stores c in m.Extensions. Fails on an unknown ability.
func SetBuildContract(m *hxdna.Manifest, c BuildContract) error {
	if c.Version == 0 {
		c.Version = BuildContractVersion
	}
	for _, a := range c.Optional {
		if _, ok := abilityCommands[a]; !ok && a != AbilityConsole {
			return fmt.Errorf("provision: unknown optional ability %q", a)
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if m.Extensions == nil {
		m.Extensions = map[string]json.RawMessage{}
	}
	m.Extensions[BuildExtension] = raw
	return nil
}

// BuildContractOf reads a worker's BuildContract from its manifest; nil when it declares none.
func BuildContractOf(m hxdna.Manifest) (*BuildContract, error) {
	raw, ok := m.Extensions[BuildExtension]
	if !ok {
		return nil, nil
	}
	var c BuildContract
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("provision: bad %s in manifest: %w", BuildExtension, err)
	}
	return &c, nil
}

// MissingCommands lists the commands m's BuildContract needs but m doesn't register — for a
// worker's own test (keep the manifest honest) and for the control plane before it offers the
// platform. Empty when nothing is missing or m declares no BuildContract.
func MissingCommands(m hxdna.Manifest) []string {
	c, err := BuildContractOf(m)
	if err != nil || c == nil {
		return nil
	}
	have := map[string]bool{}
	for _, cmd := range m.Commands {
		have[cmd.Key] = true
	}
	need := slices.Clone(RequiredCommands)
	for _, a := range c.Optional {
		if cmd, ok := abilityCommands[a]; ok {
			need = append(need, cmd)
		}
	}
	var missing []string
	for _, k := range need {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	return missing
}
