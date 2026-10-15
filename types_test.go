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

// Same reason as TestPlaybookWireNames: the control plane reads the diagnose contract and every
// diagnosis by JSON name. A DiagnoseEntry's ContractEntry fields sit at the top level, beside
// its own.
func TestDiagnoseWireNames(t *testing.T) {
	m := Manifest{
		TriageContract: []ContractEntry{{ActionKey: "triage", PossibleOutcomes: []TriageOutcomeDescriptor{
			{Value: "maintenance_required", Triggers: []string{"agent_down"}},
		}}},
		DiagnoseContract: []DiagnoseEntry{{
			ContractEntry:  ContractEntry{ActionKey: "diagnose_agent"},
			Triggers:       []string{"agent_down"},
			PossibleCauses: []CauseDescriptor{{Value: "cert_expired", FixOwner: FixOwnerPlatform, Description: "d"}},
		}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"possible_outcomes":[{"value":"maintenance_required","description":"","triggers":["agent_down"]}]`,
		`"diagnose_contract":[{"action_key":"diagnose_agent"`, `"triggers":["agent_down"]`,
		`"possible_causes":[{"value":"cert_expired","fix_owner":"platform","description":"d"}]`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("manifest JSON missing %s: %s", want, raw)
		}
	}

	res, err := json.Marshal(DiagnosisResult{
		Cause:  "cert_expired",
		Chain:  []DiagnosisStep{{Question: "Running?", Answer: DiagnosisNo, Reason: "activating"}},
		Detail: []DiagnosisDetail{{Name: "exit_error", Value: "certificate is expired"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cause":"cert_expired","chain":[{"question":"Running?","answer":"no","reason":"activating"}],"detail":[{"name":"exit_error","value":"certificate is expired"}]}`
	if got := string(res); got != want {
		t.Errorf("diagnosis JSON = %s", got)
	}
}

// A worker with no diagnoses sends no diagnose_contract, and its contracts carry no triggers.
func TestDiagnoseContractOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(Manifest{WorkerType: "w", TriageContract: []ContractEntry{{
		ActionKey: "triage", PossibleOutcomes: []TriageOutcomeDescriptor{{Value: "ok"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"diagnose_contract", "triggers", "possible_causes"} {
		if strings.Contains(string(raw), absent) {
			t.Errorf("%s serialized: %s", absent, raw)
		}
	}
}
