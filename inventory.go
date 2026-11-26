package hxdna

import "encoding/json"

// The Inventory contract is common to every line of business: "what exists, in what state,
// with which labels". A worker that can enumerate the things it manages implements one
// read-only command, list_inventory, and declares InventoryContract in its manifest. The
// control plane compares the answer with its own records to show a fleet and flag drift.
//
// Kind-specific detail (a VM's size, a switch's model) rides in Details, defined by the line
// of business that owns the kind (e.g. hxdna/provision for "vm" and "server") — so this
// core contract never learns their words.

// InventoryCommandKey is the command every InventoryContract worker registers.
const InventoryCommandKey = "list_inventory"

// InventoryContractVersion is the version this hxdna describes.
const InventoryContractVersion = 1

// InventoryContract is the manifest section of a worker that implements list_inventory.
type InventoryContract struct {
	Version int `json:"version"`
	// Kinds are the kinds of item this worker lists, e.g. ["vm"] or ["server"].
	Kinds []string `json:"kinds"`
}

// InventoryItem is one thing a worker manages, as the platform reports it now.
type InventoryItem struct {
	Kind      string   `json:"kind"`
	ID        string   `json:"id"` // the platform's own stable ID
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Addresses []string `json:"addresses,omitempty"`
	// Labels are what the platform carries on the item (tags, notes keys), e.g. the build
	// that made it — the link back to the control plane's records.
	Labels  map[string]string `json:"labels,omitempty"`
	Details json.RawMessage   `json:"details,omitempty"`
}

// InventoryResult is list_inventory's Data.
type InventoryResult struct {
	Items []InventoryItem `json:"items"`
}
