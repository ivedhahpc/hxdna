package playbook

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ivedhahpc/hxdna"
)

func strp(s string) *string { return &s }

type harness struct {
	org    uuid.UUID
	worker uuid.UUID
	store  *memStore
	w      *fakeWorker
	e      *Engine
}

func newHarness(t *testing.T, hooks Hooks) *harness {
	t.Helper()
	org := uuid.New()
	w := &fakeWorker{
		org: org,
		manifest: hxdna.Manifest{PlaybookContract: &hxdna.PlaybookContract{
			Checks:  []hxdna.ContractEntry{{ActionKey: "bmc_answering"}},
			Actions: []hxdna.ContractEntry{{ActionKey: "power_reset", InputSchema: hxdna.InputSchema{"note": "string"}}},
		}},
		answer: map[string]func(hxdna.HxCommand) (map[string]any, error){
			"bmc_answering": func(hxdna.HxCommand) (map[string]any, error) {
				return map[string]any{"passed": true, "reason": "power is on", "power": "on"}, nil
			},
			"power_reset": func(cmd hxdna.HxCommand) (map[string]any, error) {
				return map[string]any{"host": cmd.ResourceID, "note": cmd.Params["note"]}, nil
			},
		},
	}
	store := newMemStore()
	return &harness{org: org, worker: uuid.New(), store: store, w: w, e: New(store, w, Options{Hooks: hooks})}
}

func (h *harness) check(name, key string) *Step {
	return &Step{StepName: name, StepType: StepCheck, WorkerID: &h.worker, CommandKey: strp(key)}
}

func (h *harness) action(name, key string, params map[string]string) *Step {
	return &Step{StepName: name, StepType: StepAction, WorkerID: &h.worker, CommandKey: strp(key), Params: params}
}

func waitStatus(t *testing.T, s *memStore, id uuid.UUID, want string) *Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r := s.run(id); r.Status == want {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s never reached %s (is %s)", id, want, s.run(id).Status)
	return nil
}

func TestRunNowChecksThenActions(t *testing.T) {
	h := newHarness(t, Hooks{})
	p := h.store.addPlaybook(h.org, "Power cycle",
		h.check("bmc", "bmc_answering"),
		h.action("reset", "power_reset", map[string]string{"note": "was {{bmc.power}} on {{resource_id}}"}))

	run, results, err := h.e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: " node1 "})
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if got := h.store.run(run.ID).Status; got != RunSucceeded {
		t.Fatalf("status %s, want succeeded", got)
	}
	if run.ResourceID != "node1" {
		t.Fatalf("resource not trimmed: %q", run.ResourceID)
	}
	if note := results["reset"]["note"]; note != "was on on node1" {
		t.Fatalf("params not expanded: %v", note)
	}
	steps, _ := h.store.ListRunSteps(context.Background(), run.ID)
	if len(steps) != 2 || steps[0].Passed == nil || !*steps[0].Passed || steps[1].Passed != nil {
		t.Fatalf("recorded steps wrong: %+v", steps)
	}
}

func TestRunNowCheckFailsNothingActs(t *testing.T) {
	h := newHarness(t, Hooks{})
	h.w.answer["bmc_answering"] = func(hxdna.HxCommand) (map[string]any, error) {
		return map[string]any{"passed": false, "reason": "BMC not answering"}, nil
	}
	p := h.store.addPlaybook(h.org, "Power cycle", h.check("bmc", "bmc_answering"), h.action("reset", "power_reset", nil))

	run, _, err := h.e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1"})
	if !errors.Is(err, ErrChecksFailed) || !strings.Contains(err.Error(), "BMC not answering") {
		t.Fatalf("want ErrChecksFailed with the reason, got %v", err)
	}
	if got := h.store.run(run.ID).Status; got != RunCheckFailed {
		t.Fatalf("status %s, want check_failed", got)
	}
	if keys := h.w.sentKeys(); len(keys) != 1 || keys[0] != "bmc_answering" {
		t.Fatalf("an action was sent after a failed check: %v", keys)
	}
}

func TestResourceBusyRefusesBeforeAnyRun(t *testing.T) {
	h := newHarness(t, Hooks{})
	p := h.store.addPlaybook(h.org, "Power cycle", h.check("bmc", "bmc_answering"), h.action("reset", "power_reset", nil))
	other := &Run{ID: uuid.New(), OrganizationID: h.org, PlaybookName: "Other", ResourceID: "NODE1", Status: RunRunning, UpdatedAt: time.Now().UTC()}
	_ = h.store.CreateRun(context.Background(), other)

	_, _, err := h.e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1"})
	if !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("want ErrResourceBusy, got %v", err)
	}
	if len(h.w.sentKeys()) != 0 {
		t.Fatal("a command was sent to a busy device")
	}
}

func TestStartChecksThenExecuteOnce(t *testing.T) {
	h := newHarness(t, Hooks{})
	p := h.store.addPlaybook(h.org, "Power cycle", h.check("bmc", "bmc_answering"), h.action("reset", "power_reset", nil))
	person := uuid.New()

	run, err := h.e.StartChecks(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1", By: &person})
	if err != nil {
		t.Fatalf("StartChecks: %v", err)
	}
	waitStatus(t, h.store, run.ID, RunReady)

	if _, err := h.e.Execute(context.Background(), h.org, run.ID, person); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := h.e.Execute(context.Background(), h.org, run.ID, person); !errors.Is(err, ErrRunNotReady) {
		t.Fatalf("second Execute: want ErrRunNotReady, got %v", err)
	}
	done := waitStatus(t, h.store, run.ID, RunSucceeded)
	if done.ExecutedBy == nil || *done.ExecutedBy != person {
		t.Fatal("executed_by not recorded")
	}
}

func TestExecuteRefusesStaleChecks(t *testing.T) {
	h := newHarness(t, Hooks{})
	p := h.store.addPlaybook(h.org, "Power cycle", h.check("bmc", "bmc_answering"), h.action("reset", "power_reset", nil))
	old := time.Now().UTC().Add(-20 * time.Minute)
	run := &Run{ID: uuid.New(), OrganizationID: h.org, PlaybookID: &p.ID, PlaybookName: p.Name, ResourceID: "node1",
		Status: RunReady, CreatedAt: old, UpdatedAt: old}
	_ = h.store.CreateRun(context.Background(), run)

	if _, err := h.e.Execute(context.Background(), h.org, run.ID, uuid.New()); !errors.Is(err, ErrChecksStale) {
		t.Fatalf("want ErrChecksStale, got %v", err)
	}
}

func TestWithdrawnCommandFailsTheStep(t *testing.T) {
	h := newHarness(t, Hooks{})
	p := h.store.addPlaybook(h.org, "Power cycle", h.check("bmc", "bmc_answering"), h.action("reset", "power_reset", nil))
	h.w.manifest.PlaybookContract.Actions = nil // the worker no longer offers power_reset

	run, _, err := h.e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1"})
	if err == nil || !strings.Contains(err.Error(), "does not offer") {
		t.Fatalf("want a 'does not offer' failure, got %v", err)
	}
	if got := h.store.run(run.ID).Status; got != RunFailed {
		t.Fatalf("status %s, want failed", got)
	}
	for _, k := range h.w.sentKeys() {
		if k == "power_reset" {
			t.Fatal("a withdrawn command was sent")
		}
	}
}

func TestRunNowRefusesWhatDoesNotFitTheDeadline(t *testing.T) {
	h := newHarness(t, Hooks{})
	p := h.store.addPlaybook(h.org, "Power cycle", h.check("bmc", "bmc_answering"), h.action("reset", "power_reset", nil))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // needs 150 s + 60 s
	defer cancel()

	if _, _, err := h.e.RunNow(ctx, StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1"}); !IsValidation(err) {
		t.Fatalf("want a validation error, got %v", err)
	}
	if len(h.w.sentKeys()) != 0 {
		t.Fatal("ran anyway")
	}
}

func TestNotRunRecentlyStopsAutomationNotPeople(t *testing.T) {
	h := newHarness(t, Hooks{})
	p := h.store.addPlaybook(h.org, "Power cycle",
		&Step{StepName: "not_looping", StepType: StepCheck, CommandKey: strp(CheckNotRunRecently), Params: map[string]string{"max_runs": "1", "window_hours": "24"}},
		h.action("reset", "power_reset", nil))
	ctx := context.Background()

	if _, _, err := h.e.RunNow(ctx, StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, _, err := h.e.RunNow(ctx, StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "Node1"}); !errors.Is(err, ErrChecksFailed) {
		t.Fatalf("automation at the cap: want ErrChecksFailed, got %v", err)
	}
	person := uuid.New()
	if _, _, err := h.e.RunNow(ctx, StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1", By: &person}); err != nil {
		t.Fatalf("a person at the cap should still run it: %v", err)
	}
}

// ticketStep stands in for an app's own step type (HelixOps's post_comment).
type ticketStep struct{}

func (ticketStep) Type() string                  { return "post_comment" }
func (ticketStep) Phase() Phase                  { return PhaseAction }
func (ticketStep) Budget(Timeouts) time.Duration { return 30 * time.Second }
func (ticketStep) Exec(ctx context.Context, x *StepContext) (StepResult, error) {
	text, err := x.Expander(ctx, false).Text(*x.Step.Content)
	return StepResult{Data: map[string]any{"posted": text}}, err
}

func TestAppStepTypeAndHooks(t *testing.T) {
	afterCalled := false
	h := newHarness(t, Hooks{
		Placeholder: func(_ context.Context, run *Run, name string, _ bool) (string, bool, error) {
			if name == "ticket_id" {
				return run.Ext.(string), true, nil
			}
			return "", false, nil
		},
		RunByName: func(context.Context, uuid.UUID) string { return "Kam" },
		AfterActions: func(_ context.Context, _ *Run, _ []*Step, results map[string]map[string]any) error {
			afterCalled = results["comment"] != nil
			return nil
		},
	})
	if err := h.e.Register(ticketStep{}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Register(ticketStep{}); err == nil {
		t.Fatal("registering a type twice should fail")
	}
	p := h.store.addPlaybook(h.org, "Reset and note",
		h.action("reset", "power_reset", nil),
		&Step{StepName: "comment", StepType: "post_comment", Content: strp("{{run_by}} reset {{resource_id}} for {{ticket_id}}")})
	person := uuid.New()

	_, results, err := h.e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1", By: &person, Ext: "T-42"})
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if got := results["comment"]["posted"]; got != "Kam reset node1 for T-42" {
		t.Fatalf("posted %q", got)
	}
	if !afterCalled {
		t.Fatal("AfterActions not called with the results")
	}
}

func TestValidateOrder(t *testing.T) {
	h := newHarness(t, Hooks{})
	cases := map[string][]*Step{
		"empty":           nil,
		"check after act": {h.action("a", "power_reset", nil), h.check("c", "bmc_answering")},
		"unknown type":    {{StepName: "x", StepType: "teleport"}},
	}
	for name, steps := range cases {
		if err := h.e.ValidateOrder(steps); !IsValidation(err) {
			t.Errorf("%s: want a validation error, got %v", name, err)
		}
	}
	if err := h.e.ValidateOrder([]*Step{h.check("c", "bmc_answering"), h.action("a", "power_reset", nil)}); err != nil {
		t.Errorf("valid order refused: %v", err)
	}
}

func TestExpander(t *testing.T) {
	run := &Run{ResourceID: "node1", RunByName: "Kam"}
	results := map[string]map[string]any{"chk": {"gpu": map[string]any{"count": float64(8)}, "ok": true}}
	x := Expander{Run: run, Results: results}

	got, err := x.Text("{{ resource_id }} {{chk.gpu.count}} {{chk.ok}} by {{run_by}}")
	if err != nil || got != "node1 8 true by Kam" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"{{nope}}", "{{later.x}}", "{{chk.missing}}", "{{reason}}"} {
		if _, err := x.Text(bad); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
	run.ReasonFallback = "gpu_fault"
	if got, _ := x.Text("{{reason}}"); got != "gpu_fault" {
		t.Errorf("reason fallback: %q", got)
	}
	run.ReasonFallback = ""
	if got, _ := (Expander{Run: run, ForPeople: true}).Text("{{reason}}"); got != "no reason given" {
		t.Errorf("for people: %q", got)
	}
}

func TestRenameStepRefs(t *testing.T) {
	st := &Step{Content: strp("{{old.a}} {{old}} {{other.old}}"), Params: map[string]string{"p": "{{old.b.c}}"}}
	if !RenameStepRefs(st, "old", "new") {
		t.Fatal("nothing changed")
	}
	if *st.Content != "{{new.a}} {{old}} {{other.old}}" || st.Params["p"] != "{{new.b.c}}" {
		t.Fatalf("got %q %q", *st.Content, st.Params["p"])
	}
}

func TestCheckReason(t *testing.T) {
	asks := &Playbook{Name: "P", AskReason: true}
	if _, err := CheckReason(asks, nil, "", true, ""); !IsValidation(err) {
		t.Error("a person must give a reason when asked")
	}
	if r, err := CheckReason(asks, nil, "", false, ""); err != nil || r != nil {
		t.Error("automation isn't asked")
	}
	uses := []*Step{{Content: strp("why: {{reason}}")}}
	if _, err := CheckReason(&Playbook{Name: "P"}, uses, "", false, ""); !IsValidation(err) {
		t.Error("{{reason}} with nothing to read must be refused up front")
	}
	if _, err := CheckReason(&Playbook{Name: "P"}, uses, "", false, "gpu_fault"); err != nil {
		t.Errorf("fallback should do: %v", err)
	}
	if _, err := CheckReason(asks, nil, strings.Repeat("x", 501), true, ""); !IsValidation(err) {
		t.Error("too long")
	}
}

func TestNewFillsZeroTimeouts(t *testing.T) {
	e := New(newMemStore(), &fakeWorker{}, Options{Timeouts: Timeouts{Action: 40 * time.Second}})
	got := e.Timeouts()
	if got.Action != 40*time.Second || got.Check != DefaultTimeouts().Check || got.Run != DefaultTimeouts().Run {
		t.Fatalf("got %+v", got)
	}
	// A person's phase may run all of Run untouched, so it can't count as interrupted sooner.
	e = New(newMemStore(), &fakeWorker{}, Options{Timeouts: Timeouts{Run: 20 * time.Minute, StaleAfter: 5 * time.Minute}})
	if got := e.Timeouts().StaleAfter; got != 21*time.Minute {
		t.Fatalf("StaleAfter %s, want 21m", got)
	}
}

func TestRunNowRefusesWhatOutlastsStaleAfter(t *testing.T) {
	h := newHarness(t, Hooks{})
	h.e = New(h.store, h.w, Options{Timeouts: Timeouts{Check: 12 * time.Minute}}) // > the 11 min default
	p := h.store.addPlaybook(h.org, "Slow", h.check("bmc", "bmc_answering"), h.action("reset", "power_reset", nil))

	if _, _, err := h.e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1"}); err == nil || !strings.Contains(err.Error(), "StaleAfter") {
		t.Fatalf("want a StaleAfter refusal, got %v", err)
	}
	if len(h.w.sentKeys()) != 0 {
		t.Fatal("ran anyway")
	}
}

func TestStepWithoutWorkerGoesWhereTheResolverSays(t *testing.T) {
	h := newHarness(t, Hooks{})
	wyn, atn := uuid.New(), uuid.New()
	rw := &resolvingWorker{fakeWorker: h.w, bySite: map[string]uuid.UUID{"wynford": wyn, "atnorth": atn}}
	e := New(h.store, rw, Options{})
	p := h.store.addPlaybook(h.org, "Power cycle",
		&Step{StepName: "bmc", StepType: StepCheck, CommandKey: strp("bmc_answering")},
		&Step{StepName: "reset", StepType: StepAction, CommandKey: strp("power_reset")},
		&Step{StepName: "pinned", StepType: StepAction, WorkerID: &h.worker, CommandKey: strp("power_reset")})

	run, _, err := e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node1", Ext: "atnorth"})
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if got := h.w.sentTo; len(got) != 3 || got[0] != atn || got[1] != atn || got[2] != h.worker {
		t.Fatalf("sent to %v, want atnorth, atnorth, the pinned worker", got)
	}
	steps, _ := h.store.ListRunSteps(context.Background(), run.ID)
	if steps[0].InputJSON == nil || !strings.Contains(string(*steps[0].InputJSON), atn.String()) {
		t.Fatalf("recorded input does not name the resolved worker")
	}

	// A site with no worker fails the first step; nothing acts.
	h.w.sent, h.w.sentTo = nil, nil
	if _, _, err = e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node2", Ext: "nowhere"}); err == nil || !strings.Contains(err.Error(), "no Foreman worker") {
		t.Fatalf("want the resolver's error, got %v", err)
	}
	if len(h.w.sent) != 0 {
		t.Fatalf("sent %v with no worker", h.w.sent)
	}

	// Without a resolver a step with no worker fails.
	if _, _, err = h.e.RunNow(context.Background(), StartRequest{OrgID: h.org, PlaybookID: p.ID, ResourceID: "node3"}); err == nil || !strings.Contains(err.Error(), "no worker") {
		t.Fatalf("want 'no worker', got %v", err)
	}
}
