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

// The control plane reads alert types and each alert's type by JSON name. Pin them.
func TestAlertTypeWireNames(t *testing.T) {
	m := Manifest{AlertContract: &AlertPolicy{
		RecheckIntervalSeconds: 600, MaxRetries: 2,
		Types: []AlertTypeEntry{{Type: "unlisted_node", Name: "Unlisted node", Description: "d", ResourceType: "machine", MaxRetries: 3}},
	}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `"alert_contract":{"recheck_interval_seconds":600,"max_retries":2,"types":[{"type":"unlisted_node","name":"Unlisted node","description":"d","resource_type":"machine","max_retries":3}]}`
	if !strings.Contains(string(raw), want) {
		t.Errorf("manifest JSON missing %s: %s", want, raw)
	}

	card, err := json.Marshal(AlertCard{Type: "unlisted_node", ResourceID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(card), `{"type":"unlisted_node","resource_id":"n1"`) {
		t.Errorf("alert card JSON = %s", card)
	}
}

// Existing workers send neither types nor a card type: both stay absent on the wire, and an
// older manifest still decodes.
func TestAlertTypesOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(Manifest{AlertContract: &AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "types") {
		t.Errorf("empty Types serialized: %s", raw)
	}
	card, err := json.Marshal(AlertCard{ResourceID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(card), `"type"`) {
		t.Errorf("empty Type serialized: %s", card)
	}

	var m Manifest
	if err := json.Unmarshal([]byte(`{"alert_contract":{"recheck_interval_seconds":600,"max_retries":2}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.AlertContract.Types != nil || m.AlertContract.Validate() != nil {
		t.Errorf("older alert contract decoded as %+v", m.AlertContract)
	}
}

func TestAlertPolicyValidate(t *testing.T) {
	typ := func(key string, interval, retries int) AlertTypeEntry {
		return AlertTypeEntry{Type: key, Name: "n", Description: "d", RecheckIntervalSeconds: interval, MaxRetries: retries}
	}
	withSeverity := func(sev string) AlertTypeEntry {
		t := typ("port_down", 0, 0)
		t.Severity = sev
		return t
	}
	cases := []struct {
		name string
		p    AlertPolicy
		ok   bool
	}{
		{"default only", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2}, true},
		{"types on the default", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", 0, 0)}}, true},
		{"types longer than the default", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", 900, 3)}}, true},
		{"no default interval", AlertPolicy{MaxRetries: 2}, false},
		{"no default retries", AlertPolicy{RecheckIntervalSeconds: 600}, false},
		{"types without a default", AlertPolicy{Types: []AlertTypeEntry{typ("port_down", 600, 2)}}, false},
		{"shorter interval", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", 300, 0)}}, false},
		{"fewer retries", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", 0, 1)}}, false},
		{"negative timing", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", -1, 0)}}, false},
		{"duplicate key", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", 0, 0), typ("port_down", 0, 0)}}, false},
		{"empty key", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("", 0, 0)}}, false},
		{"upper case key", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("Port_Down", 0, 0)}}, false},
		{"key starts with a digit", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("1port", 0, 0)}}, false},
		{"no name", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{{Type: "port_down", Description: "d"}}}, false},
		{"no description", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{{Type: "port_down", Name: "n"}}}, false},
		{"known severity", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{withSeverity("critical")}}, true},
		{"unknown severity", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{withSeverity("High")}}, false},
		{"default interval over cap", AlertPolicy{RecheckIntervalSeconds: MaxAlertRecheckIntervalSeconds + 1, MaxRetries: 2}, false},
		{"default retries over cap", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: MaxAlertRetries + 1}, false},
		{"type interval at cap", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", MaxAlertRecheckIntervalSeconds, MaxAlertRetries)}}, true},
		{"type interval over cap", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", MaxAlertRecheckIntervalSeconds+1, 0)}}, false},
		{"type retries over cap", AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{typ("port_down", 0, MaxAlertRetries+1)}}, false},
	}
	for _, c := range cases {
		if err := c.p.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestAlertPolicyTimingFor(t *testing.T) {
	p := AlertPolicy{RecheckIntervalSeconds: 600, MaxRetries: 2, Types: []AlertTypeEntry{
		{Type: "port_down", Name: "Port down", RecheckIntervalSeconds: 900},
		{Type: "device_down", Name: "Device down", MaxRetries: 4},
		// Shorter than the default — Validate would reject it, but TimingFor still floors it.
		{Type: "flappy", Name: "Flappy", RecheckIntervalSeconds: 60, MaxRetries: 1},
	}}
	cases := []struct {
		alertType         string
		interval, retries int
		declared          bool
	}{
		{"port_down", 900, 2, true},
		{"device_down", 600, 4, true},
		{"flappy", 600, 2, true},
		{"sensor", 600, 2, false},
		{"", 600, 2, false},
	}
	for _, c := range cases {
		interval, retries, declared := p.TimingFor(c.alertType)
		if interval != c.interval || retries != c.retries || declared != c.declared {
			t.Errorf("TimingFor(%q) = %d, %d, %v; want %d, %d, %v", c.alertType, interval, retries, declared, c.interval, c.retries, c.declared)
		}
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
