package provision

import (
	"regexp"
)

// Inventory kinds this line of business owns (hxdna.InventoryItem.Kind).
const (
	KindVM     = "vm"
	KindServer = "server" // bare metal, once InstallContract exists
)

// Step is one part of a command's work, reported in order — the same {name, status, detail}
// shape everywhere so the control plane shows every build alike.
type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | error
	Detail string `json:"detail,omitempty"`
}

// StepResult is the Data of clone, configure, start, power and destroy. A long step returns
// TaskID; the control plane polls task_status (with Node) for as long as it takes.
type StepResult struct {
	InstanceID string `json:"instance_id,omitempty"`
	Node       string `json:"node,omitempty"` // the hypervisor host, when the platform needs it for task_status
	TaskID     string `json:"task_id,omitempty"`
	Steps      []Step `json:"steps,omitempty"`
}

// Task states (task_status Data: {"status", "detail"}).
const (
	TaskRunning = "running"
	TaskOK      = "ok"
	TaskError   = "error"
)

// TaskStatus is task_status's Data.
type TaskStatus struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Instance states (InstanceStatus.State).
const (
	StateCreating = "creating"
	StateStopped  = "stopped"
	StateRunning  = "running"
	StateError    = "error"
	StateGone     = "gone"
)

// Condition is an optional, observed detail of an instance (e.g. GuestReady once a guest
// agent exists). Empty in v1: images carry no guest agent.
type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status"` // true | false | unknown
	Detail string `json:"detail,omitempty"`
}

// InstanceStatus is instance_status's Data: what the platform reports now, not what was asked.
type InstanceStatus struct {
	InstanceID string      `json:"instance_id"`
	Name       string      `json:"name"`
	State      string      `json:"state"`
	Addresses  []string    `json:"addresses,omitempty"`   // as the platform knows them (v1: what was set)
	BuildID    string      `json:"build_id,omitempty"`    // from the label (BuildLabel), "" if none
	ConsoleURL string      `json:"console_url,omitempty"` // with AbilityConsole
	Conditions []Condition `json:"conditions,omitempty"`
}

// VMDetails is hxdna.InventoryItem.Details for KindVM.
type VMDetails struct {
	VCPUs    int    `json:"vcpus,omitempty"`
	MemoryMB int    `json:"memory_mb,omitempty"`
	DiskGB   int    `json:"disk_gb,omitempty"`
	Image    string `json:"image,omitempty"` // as the platform knows it
	Node     string `json:"node,omitempty"`
}

// LabelBuild is the inventory label key holding the build that made an item.
const LabelBuild = "provision-build"

// BuildLabel is the text a worker writes onto every machine a build makes (VM notes, a host
// parameter): "provision-build:<build_id>". Workers act only on machines carrying the asking
// build's label, and list_inventory returns it so the control plane can match machines to
// its records.
func BuildLabel(buildID string) string { return LabelBuild + ":" + buildID }

var buildLabelRe = regexp.MustCompile(`provision-build:([A-Za-z0-9-]+)`)

// BuildIDFrom finds a build label in free text (e.g. a VM's notes); "" when there is none.
func BuildIDFrom(text string) string {
	if m := buildLabelRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}
