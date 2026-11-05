package playbook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Timeouts bound a run. A check gathers evidence over SSH (it can take a minute or more), so it gets
// a triage-scale budget; an action gets less. Each phase of a person's run must fit Run; a run
// started with RunNow must fit its caller's deadline.
type Timeouts struct {
	Check  time.Duration
	Action time.Duration
	// Run is how long each phase of a person's run (StartChecks, Execute) may take.
	Run time.Duration
	// ChecksValidFor is how long a run's passed checks stand for Execute: the device's state is what
	// made the actions safe, and it moves on.
	ChecksValidFor time.Duration
	// StaleAfter: a run in progress untouched for longer has been interrupted (its goroutine is
	// gone), and another run may take its device. At least the longest a run can go untouched:
	// New keeps it above Run, and RunNow refuses a playbook whose worst case is longer.
	StaleAfter time.Duration
}

// DefaultTimeouts are HelixOps's: check 150 s, action 60 s, 10 min per phase, checks valid 15 min.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Check:          150 * time.Second,
		Action:         60 * time.Second,
		Run:            10 * time.Minute,
		ChecksValidFor: 15 * time.Minute,
		StaleAfter:     11 * time.Minute,
	}
}

// Hooks are the app's say in a run. Every field is optional.
type Hooks struct {
	// RunByName names the person behind a run for {{run_by}} (userID is ExecutedBy, else
	// CreatedBy). Default "someone" — never an email, which would land in chat or a ticket.
	// Automation (no person) is always "Automation".
	RunByName func(ctx context.Context, userID uuid.UUID) string
	// Placeholder resolves the app's own bare {{names}}; ok false means it doesn't know name.
	Placeholder func(ctx context.Context, run *Run, name string, forPeople bool) (value string, ok bool, err error)
	// ValidateSteps adds the app's own step-order rules (HelixOps: set_bucket last, one
	// create_ticket) to the engine's.
	ValidateSteps func(steps []*Step) error
	// BeforeActions runs as the action phase starts (HelixOps: whether the run escalates).
	BeforeActions func(ctx context.Context, run *Run, steps []*Step)
	// AfterActions runs once every action step succeeded, before the run is marked succeeded
	// (HelixOps: move the linked ticket to its bucket). An error is logged; the run still succeeded.
	AfterActions func(ctx context.Context, run *Run, steps []*Step, results map[string]map[string]any) error
}

// Options configure an Engine. Zero values take the defaults.
type Options struct {
	Timeouts Timeouts
	Hooks    Hooks
	Logger   *slog.Logger
}

// Engine runs playbooks. Safe for concurrent use once its step types are registered.
type Engine struct {
	store      Store
	dispatcher Dispatcher
	hooks      Hooks
	t          Timeouts
	log        *slog.Logger
	types      map[string]StepType
}

// New returns an engine with the check and action step types registered.
func New(store Store, dispatcher Dispatcher, opts Options) *Engine {
	e := &Engine{
		store:      store,
		dispatcher: dispatcher,
		hooks:      opts.Hooks,
		t:          opts.Timeouts,
		log:        opts.Logger,
		types:      map[string]StepType{},
	}
	// A field left zero takes its default.
	def := DefaultTimeouts()
	for _, f := range []struct{ v, d *time.Duration }{
		{&e.t.Check, &def.Check}, {&e.t.Action, &def.Action}, {&e.t.Run, &def.Run},
		{&e.t.ChecksValidFor, &def.ChecksValidFor}, {&e.t.StaleAfter, &def.StaleAfter},
	} {
		if *f.v <= 0 {
			*f.v = *f.d
		}
	}
	// A person's phase can go untouched for all of Run; counted as interrupted sooner, another run
	// could take the device while it is still acting.
	if e.t.StaleAfter < e.t.Run+time.Minute {
		e.t.StaleAfter = e.t.Run + time.Minute
	}
	if e.log == nil {
		e.log = slog.Default()
	}
	e.types[StepCheck] = checkStep{}
	e.types[StepAction] = actionStep{}
	return e
}

// Register adds an app's step type. Call before the engine runs anything.
func (e *Engine) Register(t StepType) error {
	if _, dup := e.types[t.Type()]; dup {
		return fmt.Errorf("step type %q is already registered", t.Type())
	}
	e.types[t.Type()] = t
	return nil
}

// Timeouts returns the engine's timeouts.
func (e *Engine) Timeouts() Timeouts { return e.t }

// StepTypes returns the registered step type names.
func (e *Engine) StepTypes() []string {
	names := make([]string, 0, len(e.types))
	for n := range e.types {
		names = append(names, n)
	}
	return names
}

func (e *Engine) staleBefore() time.Time {
	return time.Now().UTC().Add(-e.t.StaleAfter)
}

// ─── Rules ────────────────────────────────────────────────────────────────────

// ValidateOrder requires a known type for every step and every check-phase step before the first
// action-phase step — checks gate the actions, so one after an action would run only after
// something had already changed — then the app's own rules (Hooks.ValidateSteps).
func (e *Engine) ValidateOrder(steps []*Step) error {
	if len(steps) == 0 {
		return validationErr("this playbook has no steps")
	}
	seenAction := false
	for _, st := range steps {
		t, ok := e.types[st.StepType]
		if !ok {
			return validationErr(fmt.Sprintf("step %q has unknown type %q", st.StepName, st.StepType))
		}
		if t.Phase() != PhaseCheck {
			seenAction = true
		} else if seenAction {
			return validationErr(fmt.Sprintf("check step %q comes after an action — move every check before the first action", st.StepName))
		}
	}
	if e.hooks.ValidateSteps != nil {
		return e.hooks.ValidateSteps(steps)
	}
	return nil
}

// WorstCase is the longest a run of steps can take, each step at its full budget. Call after
// ValidateOrder (an unknown type counts as nothing).
func (e *Engine) WorstCase(steps []*Step) time.Duration {
	var d time.Duration
	for _, st := range steps {
		if t, ok := e.types[st.StepType]; ok {
			d += t.Budget(e.t)
		}
	}
	return d
}

// phaseSteps are the steps of one phase, in order.
func (e *Engine) phaseSteps(steps []*Step, phase Phase) []*Step {
	var out []*Step
	for _, st := range steps {
		if t, ok := e.types[st.StepType]; ok && t.Phase() == phase {
			out = append(out, st)
		}
	}
	return out
}

// fitsPhase refuses a person's run whose phase could take longer than Timeouts.Run — cut off
// mid-action, the run would stop partway with the device half changed.
func (e *Engine) fitsPhase(steps []*Step, phase Phase) error {
	if need := e.WorstCase(e.phaseSteps(steps, phase)); need > e.t.Run {
		what := "actions"
		if phase == PhaseCheck {
			what = "checks"
		}
		return validationErr(fmt.Sprintf("this playbook's %s can take up to %s, more than the %s a run is allowed — use fewer steps", what, need.Round(time.Second), e.t.Run))
	}
	return nil
}

// ResourceBusy is a quick look before starting work on resourceID: ErrResourceBusy (with the run
// acting on it) when a run is acting on it now, else nil. ClaimAction is what guarantees it; this
// only saves a run that would be refused.
func (e *Engine) ResourceBusy(ctx context.Context, orgID uuid.UUID, resourceID string) error {
	if resourceID == "" {
		return nil
	}
	holder, err := e.store.ActingOn(ctx, orgID, resourceID, e.staleBefore())
	if err != nil {
		return fmt.Errorf("check whether %s is busy: %w", resourceID, err)
	}
	if holder != nil {
		return resourceBusy(holder)
	}
	return nil
}

// ─── Runs ─────────────────────────────────────────────────────────────────────

// StartRequest starts a run of a playbook against one device.
type StartRequest struct {
	OrgID      uuid.UUID
	PlaybookID uuid.UUID
	ResourceID string
	// By is the person running it; nil when automation runs it.
	By *uuid.UUID
	// Reason is why the person is running it — required when the playbook asks for one.
	Reason string
	// ReasonFallback is what {{reason}} reads when there's no reason (see Run.ReasonFallback).
	ReasonFallback string
	// Ext is the app's own data for the new run (see Run.Ext); the app's Store reads it on CreateRun.
	Ext any
}

// load reads and validates the playbook and its steps for a new run.
func (e *Engine) load(ctx context.Context, req StartRequest) (*Playbook, []*Step, string, error) {
	resourceID := strings.TrimSpace(req.ResourceID)
	if resourceID == "" {
		return nil, nil, "", validationErr("resource_id is required")
	}
	p, err := e.store.GetPlaybook(ctx, req.OrgID, req.PlaybookID)
	if err != nil {
		return nil, nil, "", err
	}
	if p == nil {
		return nil, nil, "", ErrNotFound
	}
	steps, err := e.store.ListSteps(ctx, req.OrgID, p.ID)
	if err != nil {
		return nil, nil, "", err
	}
	if err := e.ValidateOrder(steps); err != nil {
		return nil, nil, "", err
	}
	return p, steps, resourceID, nil
}

func (e *Engine) newRun(ctx context.Context, req StartRequest, p *Playbook, steps []*Step, resourceID string, executedBy *uuid.UUID) (*Run, error) {
	reason, err := CheckReason(p, steps, req.Reason, req.By != nil, req.ReasonFallback)
	if err != nil {
		return nil, err
	}
	// No point checking a device another run is changing.
	if err := e.ResourceBusy(ctx, req.OrgID, resourceID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	run := &Run{
		ID:             uuid.New(),
		OrganizationID: req.OrgID,
		PlaybookID:     &p.ID,
		PlaybookName:   p.Name,
		ResourceID:     resourceID,
		Status:         RunChecking,
		CreatedBy:      req.By,
		ExecutedBy:     executedBy,
		Reason:         reason,
		CreatedAt:      now,
		UpdatedAt:      now,
		ReasonFallback: req.ReasonFallback,
		Ext:            req.Ext,
	}
	if err := e.store.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

// StartChecks is a person's first click: it creates a run in checking and runs its check steps in
// the background. The caller polls Detail until the run reaches ready or check_failed; Execute then
// runs the actions. A playbook with no checks is ready straight away.
func (e *Engine) StartChecks(ctx context.Context, req StartRequest) (*Run, error) {
	p, steps, resourceID, err := e.load(ctx, req)
	if err != nil {
		return nil, err
	}
	// Both phases, before any check runs: no point checking a device for actions that can't run.
	for _, ph := range []Phase{PhaseCheck, PhaseAction} {
		if err := e.fitsPhase(steps, ph); err != nil {
			return nil, err
		}
	}
	run, err := e.newRun(ctx, req, p, steps, resourceID, nil)
	if err != nil {
		return nil, err
	}
	// The checks get their own copy: the caller is about to answer with run.
	bg := *run
	e.background(ctx, &bg, func(bctx context.Context) { e.runChecks(bctx, &bg, steps, true) })
	return run, nil
}

// Execute is a person's second click: it runs a ready run's action steps in the background. Only a
// run whose checks all passed can be run, and only once — a second click finds it no longer ready.
func (e *Engine) Execute(ctx context.Context, orgID, runID, executedBy uuid.UUID) (*Run, error) {
	run, err := e.store.GetRun(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrNotFound
	}
	if run.PlaybookID == nil {
		return nil, validationErr("this run's playbook has been deleted")
	}
	p, err := e.store.GetPlaybook(ctx, orgID, *run.PlaybookID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, validationErr("this run's playbook has been deleted")
	}
	steps, err := e.store.ListSteps(ctx, orgID, p.ID)
	if err != nil {
		return nil, err
	}
	if err := e.ValidateOrder(steps); err != nil {
		return nil, err
	}
	if err := e.fitsPhase(steps, PhaseAction); err != nil {
		return nil, err
	}
	// The checks stand only while recent — a ready run's updated_at is when they finished — and
	// while the playbook is as it was when the run started (created_at): an edit made while they
	// were still running is a change too.
	if run.Status == RunReady && (time.Since(run.UpdatedAt) > e.t.ChecksValidFor || changedSince(p, steps, run.CreatedAt)) {
		return nil, ErrChecksStale
	}
	// Two clicks on Run can't both fire the actions, and nothing else acts on the device meanwhile.
	checkedAfter := time.Now().UTC().Add(-e.t.ChecksValidFor)
	if err := e.claim(ctx, run, RunReady, &executedBy, &checkedAfter); err != nil {
		return nil, err
	}
	run.Status = RunRunning
	run.ExecutedBy = &executedBy

	bg := *run
	e.background(ctx, &bg, func(bctx context.Context) { e.runActions(bctx, &bg, steps) })
	return run, nil
}

// RunNow runs a playbook start to finish in the caller's goroutine — checks, then, if every check
// passed, the actions — the same run and record a person's StartChecks + Execute produces. For
// automation and requests from another system (HelixOps's resolver; a HelixOps request to an app).
//
// The whole run happens inside ctx's deadline: one whose worst case doesn't fit is refused up
// front rather than cut off mid-action. ErrChecksFailed means a check stopped it before anything
// acted; ErrResourceBusy that another run was acting on the device (or acted during these checks),
// so nothing acted and the caller may try again later. Any other error with a run means it failed
// partway, and its steps record how far it got. results are every step's result by name.
func (e *Engine) RunNow(ctx context.Context, req StartRequest) (*Run, map[string]map[string]any, error) {
	p, steps, resourceID, err := e.load(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	need := e.WorstCase(steps)
	if deadline, ok := ctx.Deadline(); ok {
		if budget := time.Until(deadline); need > budget {
			return nil, nil, validationErr(fmt.Sprintf("playbook %s can take up to %s, more than the %s this run is allowed — use fewer steps", p.Name, need.Round(time.Second), budget.Round(time.Second)))
		}
	}
	// A run untouched for StaleAfter counts as interrupted, and another run may take its device —
	// so one that may take longer must not start.
	if need > e.t.StaleAfter {
		return nil, nil, fmt.Errorf("playbook %s can take up to %s, longer than a run may go untouched (Timeouts.StaleAfter %s) — raise StaleAfter or use fewer steps", p.Name, need.Round(time.Second), e.t.StaleAfter)
	}
	run, err := e.newRun(ctx, req, p, steps, resourceID, req.By)
	if err != nil {
		return nil, nil, err
	}
	if failed := e.runChecks(ctx, run, steps, false); failed != "" {
		return run, nil, fmt.Errorf("%w: %s", ErrChecksFailed, failed)
	}
	if err := e.claim(ctx, run, RunChecking, nil, nil); err != nil {
		// Nothing acted: the run ends where its checks did, saying why.
		e.finish(ctx, run, RunCheckFailed, err.Error())
		return run, nil, err
	}
	failed, results := e.runActions(ctx, run, steps)
	if failed != "" {
		return run, results, fmt.Errorf("playbook %s failed at %s", p.Name, failed)
	}
	return run, results, nil
}

// Detail returns a run and its attempted steps, failing it first if it was interrupted.
func (e *Engine) Detail(ctx context.Context, orgID, runID uuid.UUID) (*Run, []*RunStep, error) {
	run, err := e.store.GetRun(ctx, orgID, runID)
	if err != nil {
		return nil, nil, err
	}
	if run == nil {
		return nil, nil, ErrNotFound
	}
	e.FailIfInterrupted(ctx, run)
	steps, err := e.store.ListRunSteps(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	return run, steps, nil
}

// FailIfInterrupted fails a run left in checking/running past StaleAfter — the goroutine that owned
// it is gone (the backend restarted mid-run), so nothing will ever finish it. Done lazily on read,
// which is when anyone is waiting on it; a checking run fails as check_failed so the device can
// simply be checked again.
func (e *Engine) FailIfInterrupted(ctx context.Context, run *Run) {
	failStatus := RunCheckFailed
	switch run.Status {
	case RunChecking:
	case RunRunning:
		failStatus = RunFailed
	default:
		return
	}
	cutoff := e.staleBefore()
	if !run.UpdatedAt.Before(cutoff) {
		return
	}
	changed, err := e.store.FailIfStale(ctx, run.ID, run.Status, failStatus, InterruptedMsg, cutoff)
	if err != nil {
		e.log.WarnContext(ctx, "playbook: failed to mark interrupted run", "run_id", run.ID, "err", err)
		return
	}
	if changed {
		e.log.WarnContext(ctx, "playbook: run was interrupted", "run_id", run.ID, "status", run.Status)
		m := InterruptedMsg
		run.Status, run.ErrorMessage = failStatus, &m
	}
}

// claim starts run's action phase from status from: nil when it holds its resource,
// ErrResourceBusy when another run is in the way, ErrRunNotReady when the run had moved on (or its
// checks are older than checkedAfter).
func (e *Engine) claim(ctx context.Context, run *Run, from string, executedBy *uuid.UUID, checkedAfter *time.Time) error {
	claimed, holder, err := e.store.ClaimAction(ctx, ActionClaim{
		RunID: run.ID, OrgID: run.OrganizationID, From: from,
		ExecutedBy: executedBy, CheckedAfter: checkedAfter, StaleBefore: e.staleBefore(),
	})
	switch {
	case err != nil:
		return fmt.Errorf("start playbook actions: %w", err)
	case claimed:
		return nil
	case holder != nil:
		return resourceBusy(holder)
	default:
		return ErrRunNotReady
	}
}

// background runs fn detached from the caller (which returns straight away), within Timeouts.Run.
// A panic fails the run instead of leaving it stuck in checking/running.
func (e *Engine) background(ctx context.Context, run *Run, fn func(context.Context)) {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.t.Run)
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				e.log.ErrorContext(bctx, "playbook run panicked", "run_id", run.ID, "panic", r, "stack", string(debug.Stack()))
				_ = e.store.SetRunStatus(context.WithoutCancel(bctx), run.ID, RunFailed, fmt.Sprintf("internal error: %v", r), true)
			}
		}()
		fn(bctx)
	}()
}

// runByName names who is behind a run for {{run_by}}.
func (e *Engine) runByName(ctx context.Context, run *Run) string {
	id := run.ExecutedBy
	if id == nil {
		id = run.CreatedBy
	}
	if id == nil {
		return "Automation"
	}
	if e.hooks.RunByName != nil {
		if name := e.hooks.RunByName(ctx, *id); name != "" {
			return name
		}
	}
	return "someone"
}

// runChecks runs the check steps in order. The first check that errors or doesn't pass stops the
// run at check_failed and is returned as the reason; "" means every check passed. markReady leaves a
// passing run ready for a person to run — RunNow moves straight on to the actions.
func (e *Engine) runChecks(ctx context.Context, run *Run, steps []*Step, markReady bool) string {
	run.RunByName = e.runByName(ctx, run)
	results := map[string]map[string]any{}
	for _, step := range e.phaseSteps(steps, PhaseCheck) {
		res, err := e.execStep(ctx, run, step, results)
		if err != nil {
			msg := fmt.Sprintf("%s: %v", step.StepName, err)
			e.finish(ctx, run, RunCheckFailed, msg)
			return msg
		}
		results[step.StepName] = res.Data
		if !res.Passed && !res.Skipped {
			reason, _ := res.Data["reason"].(string)
			if reason == "" {
				reason = "did not pass"
			}
			msg := fmt.Sprintf("%s: %s", step.StepName, reason)
			e.finish(ctx, run, RunCheckFailed, msg)
			return msg
		}
	}
	if markReady {
		if err := e.store.SetRunStatus(ctx, run.ID, RunReady, "", false); err != nil {
			e.log.ErrorContext(ctx, "playbook: failed to mark run ready", "run_id", run.ID, "err", err)
		}
	}
	return ""
}

// runActions runs the action-phase steps in order, stopping at the first failure, which it returns
// as the reason ("" on success). Earlier checks' results are reloaded from the run's own record so
// later steps can reference them; results are every step's result by name.
func (e *Engine) runActions(ctx context.Context, run *Run, steps []*Step) (string, map[string]map[string]any) {
	run.RunByName = e.runByName(ctx, run)
	if e.hooks.BeforeActions != nil {
		e.hooks.BeforeActions(ctx, run, steps)
	}
	results := map[string]map[string]any{}
	if prior, err := e.store.ListRunSteps(ctx, run.ID); err == nil {
		for _, rs := range prior {
			if rs.OutputJSON != nil {
				var data map[string]any
				if json.Unmarshal(*rs.OutputJSON, &data) == nil {
					results[rs.StepName] = data
				}
			}
		}
	}

	for _, step := range e.phaseSteps(steps, PhaseAction) {
		res, err := e.execStep(ctx, run, step, results)
		if err != nil {
			msg := fmt.Sprintf("%s: %v", step.StepName, err)
			e.finish(ctx, run, RunFailed, msg)
			return msg, results
		}
		results[step.StepName] = res.Data
	}

	if e.hooks.AfterActions != nil {
		if err := e.hooks.AfterActions(ctx, run, steps, results); err != nil {
			e.log.WarnContext(ctx, "playbook: run succeeded but its after-actions hook failed", "run_id", run.ID, "err", err)
		}
	}
	e.finish(ctx, run, RunSucceeded, "")
	return "", results
}

func (e *Engine) finish(ctx context.Context, run *Run, status, errMsg string) {
	run.Status = status
	if err := e.store.SetRunStatus(ctx, run.ID, status, errMsg, true); err != nil {
		e.log.ErrorContext(ctx, "playbook: failed to record run status", "run_id", run.ID, "status", status, "err", err)
	}
}

// execStep runs one step and records it.
func (e *Engine) execStep(ctx context.Context, run *Run, step *Step, results map[string]map[string]any) (StepResult, error) {
	started := time.Now().UTC()
	var res StepResult
	var err error
	if t, ok := e.types[step.StepType]; ok {
		res, err = t.Exec(ctx, &StepContext{Engine: e, Run: run, Step: step, Results: results})
	} else {
		err = fmt.Errorf("unknown step type %q", step.StepType)
	}

	var passed *bool
	if t, ok := e.types[step.StepType]; ok && t.Phase() == PhaseCheck && err == nil && !res.Skipped {
		p := res.Passed
		passed = &p
	}
	completed := time.Now().UTC()
	rs := &RunStep{
		ID:          uuid.New(),
		RunID:       run.ID,
		OrderIndex:  step.OrderIndex,
		StepName:    step.StepName,
		StepType:    step.StepType,
		Status:      StepSucceeded,
		Passed:      passed,
		InputJSON:   rawOrNil(res.Input),
		OutputJSON:  rawOrNil(res.Data),
		StartedAt:   started,
		CompletedAt: &completed,
		CreatedAt:   completed,
	}
	if res.Skipped {
		rs.Status = StepSkipped
	}
	if err != nil {
		rs.Status = StepFailed
		msg := err.Error()
		rs.ErrorMessage = &msg
	}
	if cerr := e.store.CreateRunStep(ctx, rs); cerr != nil {
		e.log.ErrorContext(ctx, "playbook: failed to record step", "run_id", run.ID, "step", step.StepName, "err", cerr)
	}
	return res, err
}

func rawOrNil(v map[string]any) *json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	raw := json.RawMessage(b)
	return &raw
}
