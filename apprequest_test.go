package hxdna

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func diagnoseCap() AppCapability {
	return AppCapability{
		Key: "diagnose_node", DisplayName: "Diagnose node", Kind: AppCapabilityRead,
		Inputs:      InputSchema{"node": "string, required"},
		ResultKinds: []AppResultKind{AppResultDiagnosed, AppResultNeedsHuman},
		Outcomes:    []string{"healthy", "needs_rebuild"},
		MaxHours:    4,
	}
}

func rebuildCap() AppCapability {
	return AppCapability{
		Key: "rebuild_node", Kind: AppCapabilityWrite,
		ResultKinds: []AppResultKind{AppResultResolved, AppResultNeedsHuman},
		MaxHours:    24,
	}
}

func TestAppCapabilityValidate(t *testing.T) {
	orderCap := AppCapability{Key: "order", Kind: AppCapabilityTask, ResultKinds: []AppResultKind{AppResultResolved, AppResultNeedsHuman}, MaxHours: 720}
	for _, c := range []AppCapability{diagnoseCap(), rebuildCap(), orderCap} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s refused: %v", c.Key, err)
		}
	}
	bad := map[string]func(*AppCapability){
		"key":              func(c *AppCapability) { c.Key = "Diagnose-Node" },
		"kind":             func(c *AppCapability) { c.Kind = "admin" },
		"no kinds":         func(c *AppCapability) { c.ResultKinds = nil },
		"unknown kind":     func(c *AppCapability) { c.ResultKinds = append(c.ResultKinds, "fixed") },
		"dup kind":         func(c *AppCapability) { c.ResultKinds = append(c.ResultKinds, AppResultDiagnosed) },
		"read resolves":    func(c *AppCapability) { c.ResultKinds = append(c.ResultKinds, AppResultResolved) },
		"diagnosed no out": func(c *AppCapability) { c.Outcomes = nil },
		"bad outcome":      func(c *AppCapability) { c.Outcomes = []string{"Needs Rebuild"} },
		"dup outcome":      func(c *AppCapability) { c.Outcomes = []string{"healthy", "healthy"} },
		"hours":            func(c *AppCapability) { c.MaxHours = 0 },
	}
	for name, change := range bad {
		c := diagnoseCap()
		change(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAppRequestResultValidate(t *testing.T) {
	d, w := diagnoseCap(), rebuildCap()
	ok := []struct {
		r AppRequestResult
		c *AppCapability
	}{
		{AppRequestResult{Kind: AppResultDiagnosed, Outcome: "needs_rebuild", Evidence: json.RawMessage(`{"disk":"sda"}`)}, &d},
		{AppRequestResult{Kind: AppResultNeedsHuman, Note: "BMC not answering"}, &d},
		{AppRequestResult{Kind: AppResultRejected, Note: "not installed"}, &d}, // rejected always allowed
		{AppRequestResult{Kind: AppResultResolved}, &w},
		{AppRequestResult{Kind: AppResultResolved}, nil},
	}
	for i, c := range ok {
		if err := c.r.Validate(c.c); err != nil {
			t.Errorf("ok %d refused: %v", i, err)
		}
	}
	bad := []struct {
		name string
		r    AppRequestResult
		c    *AppCapability
	}{
		{"unknown kind", AppRequestResult{Kind: "fixed"}, nil},
		{"diagnosed without outcome", AppRequestResult{Kind: AppResultDiagnosed}, nil},
		{"outcome on resolved", AppRequestResult{Kind: AppResultResolved, Outcome: "healthy"}, nil},
		{"needs_human without note", AppRequestResult{Kind: AppResultNeedsHuman}, nil},
		{"evidence not json", AppRequestResult{Kind: AppResultResolved, Evidence: json.RawMessage(`{not`)}, nil},
		{"evidence too big", AppRequestResult{Kind: AppResultResolved, Evidence: json.RawMessage(`"` + strings.Repeat("x", MaxAppEvidenceLen) + `"`)}, nil},
		{"kind not declared", AppRequestResult{Kind: AppResultResolved}, &d},
		{"outcome not declared", AppRequestResult{Kind: AppResultDiagnosed, Outcome: "bad_nic"}, &d},
	}
	for _, c := range bad {
		if err := c.r.Validate(c.c); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestAppRequestAndUpdateValidate(t *testing.T) {
	r := AppRequest{RequestID: "r1", OrganizationID: "o1", Capability: "diagnose_node", ReplyBy: time.Now().Add(time.Hour)}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*AppRequest){
		"no id":      func(r *AppRequest) { r.RequestID = " " },
		"no org":     func(r *AppRequest) { r.OrganizationID = "" },
		"bad cap":    func(r *AppRequest) { r.Capability = "../x" },
		"no replyby": func(r *AppRequest) { r.ReplyBy = time.Time{} },
	} {
		c := r
		change(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if (AppRequestUpdate{Text: "rebuild scheduled"}).Validate() != nil {
		t.Error("good update refused")
	}
	if (AppRequestUpdate{Text: "  "}).Validate() == nil || (AppRequestUpdate{Text: strings.Repeat("x", MaxAppUpdateLen+1)}).Validate() == nil {
		t.Error("bad update accepted")
	}
}

func TestConsoleSignature(t *testing.T) {
	key := []byte("app-signing-key")
	body := []byte(`{"request_id":"r1"}`)
	now := time.Unix(1_800_000_000, 0)
	sig, ts := SignConsoleRequest(key, body, now)
	if !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("signature %q", sig)
	}
	if err := VerifyConsoleRequest(key, body, sig, ts, now.Add(time.Minute)); err != nil {
		t.Fatalf("good signature refused: %v", err)
	}
	cases := map[string]error{
		"other key":   VerifyConsoleRequest([]byte("other"), body, sig, ts, now),
		"body edited": VerifyConsoleRequest(key, []byte(`{"request_id":"r2"}`), sig, ts, now),
		"replayed":    VerifyConsoleRequest(key, body, sig, ts, now.Add(MaxConsoleSignatureSkew+time.Second)),
		"future":      VerifyConsoleRequest(key, body, sig, ts, now.Add(-MaxConsoleSignatureSkew-time.Second)),
		"ts swapped":  VerifyConsoleRequest(key, body, sig, "1800000001", now),
		"no prefix":   VerifyConsoleRequest(key, body, strings.TrimPrefix(sig, "sha256="), ts, now),
		"no key":      VerifyConsoleRequest(nil, body, sig, ts, now),
		"bad ts":      VerifyConsoleRequest(key, body, sig, "soon", now),
	}
	for name, err := range cases {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAppRequestTicketJSON(t *testing.T) {
	r := AppRequest{RequestID: "r1", OrganizationID: "o1", Capability: "diagnose_node", ReplyBy: time.Now(),
		Context: AppRequestContext{Ticket: &AppRequestTicket{Number: "48213", URL: "https://sor.example/t/48213"}}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back AppRequest
	if err := json.Unmarshal(b, &back); err != nil || back.Context.Ticket == nil || back.Context.Ticket.Number != "48213" {
		t.Errorf("ticket lost: %s", b)
	}
	// No ticket: the field is left out, so apps built on v1.20 read the same request.
	b, _ = json.Marshal(AppRequest{RequestID: "r1"})
	if strings.Contains(string(b), "ticket") {
		t.Errorf("empty ticket sent: %s", b)
	}
}

// v1.22: a request carries the ticket's title and description; both are left out when empty, so a
// request from an older console reads as before, and an older app simply ignores them.
func TestAppRequestTicketTitleDescription(t *testing.T) {
	desc := "Order: ORD-10421\nCustomer: acme-ai\nItem: gpu-h100-8x x 2"
	r := AppRequest{RequestID: "r1", Context: AppRequestContext{Ticket: &AppRequestTicket{
		Number: "48213", Title: "Order ORD-10421 — Acme AI", Description: desc}}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back AppRequest
	if err := json.Unmarshal(b, &back); err != nil || back.Context.Ticket.Title != "Order ORD-10421 — Acme AI" || back.Context.Ticket.Description != desc {
		t.Fatalf("title/description lost: %s", b)
	}
	b, _ = json.Marshal(AppRequest{Context: AppRequestContext{Ticket: &AppRequestTicket{Number: "1"}}})
	if strings.Contains(string(b), "title") || strings.Contains(string(b), "description") {
		t.Errorf("empty title/description sent: %s", b)
	}
}

func TestTicketDescription(t *testing.T) {
	if got := TicketDescription("short"); got != "short" {
		t.Errorf("short text changed: %q", got)
	}
	long := strings.Repeat("é", TicketDescriptionMax) // 2 bytes each — the cut lands mid-character
	got := TicketDescription(long)
	if len(got) > TicketDescriptionMax || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Errorf("cut to %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}
