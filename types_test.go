package hxdna

import (
	"encoding/json"
	"strings"
	"testing"
)

// The control plane parses these by JSON name, so a renamed tag would compile everywhere and
// silently empty every playbook. Pin the wire names.
func TestPlaybookWireNames(t *testing.T) {
	m := Manifest{PlaybookContract: &PlaybookContract{
		Checks:  []ContractEntry{{ActionKey: "ready"}},
		Actions: []ContractEntry{{ActionKey: "relist", InputSchema: InputSchema{"status": "string"}}},
	}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"playbook_contract":{`, `"checks":[{"action_key":"ready"`, `"actions":[{"action_key":"relist"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("manifest JSON missing %s: %s", want, raw)
		}
	}

	res, err := json.Marshal(PlaybookCheckResult{Passed: false, Reason: "why"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res); got != `{"passed":false,"reason":"why"}` {
		t.Errorf("check result JSON = %s", got)
	}
}

// A worker with nothing for playbooks sends no playbook_contract at all, and a control plane
// reading an older worker's manifest gets nil — both sides handle absence, not an empty object.
func TestPlaybookContractOmittedWhenNil(t *testing.T) {
	raw, err := json.Marshal(Manifest{WorkerType: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "playbook_contract") {
		t.Errorf("nil PlaybookContract serialized: %s", raw)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(`{"worker_type":"w","version":"1","commands":[]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.PlaybookContract != nil {
		t.Error("absent playbook_contract decoded as non-nil")
	}
}
