package playbook

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Phase is when a step type runs. Every check-phase step comes before the first action-phase step.
type Phase int

const (
	// PhaseCheck steps decide whether the actions may run; the first one that fails stops the run.
	PhaseCheck Phase = iota
	// PhaseAction steps run only after every check passed.
	PhaseAction
)

// StepType runs one kind of step. check and action come with the engine; an app registers its own
// (Engine.Register) — HelixOps's ticket, bucket and chat steps, for example.
type StepType interface {
	// Type is the step_type it runs.
	Type() string
	Phase() Phase
	// Budget is the longest one step of this type can take — what the engine adds up to keep a
	// run inside its time window.
	Budget(t Timeouts) time.Duration
	// Exec runs the step. An error fails the step (and the run); for a check, Passed false stops
	// the run with Data["reason"].
	Exec(ctx context.Context, x *StepContext) (StepResult, error)
}

// StepContext is what a step runs with.
type StepContext struct {
	Engine *Engine
	Run    *Run
	Step   *Step
	// Results are the earlier steps' results in this run, by step name.
	Results map[string]map[string]any
}

// Expander fills placeholders for this step: the engine's names, the app's (Hooks.Placeholder),
// and earlier steps' results.
func (x *StepContext) Expander(ctx context.Context, forPeople bool) Expander {
	ex := Expander{Run: x.Run, Results: x.Results, ForPeople: forPeople}
	if h := x.Engine.hooks.Placeholder; h != nil {
		ex.Extra = func(name string, forPeople bool) (string, bool, error) {
			return h(ctx, x.Run, name, forPeople)
		}
	}
	return ex
}

// StepResult is what a step did.
type StepResult struct {
	// Input is recorded as what the step was given (input_json).
	Input map[string]any
	// Data is its result (output_json), which later steps can reference.
	Data map[string]any
	// Passed is a check's verdict.
	Passed bool
	// Skipped records the step as skipped: it had nothing to do on this run.
	Skipped bool
}

// ─── check ────────────────────────────────────────────────────────────────────

type checkStep struct{}

func (checkStep) Type() string                    { return StepCheck }
func (checkStep) Phase() Phase                    { return PhaseCheck }
func (checkStep) Budget(t Timeouts) time.Duration { return t.Check }

func (checkStep) Exec(ctx context.Context, x *StepContext) (StepResult, error) {
	if IsBuiltInCheck(x.Step) {
		in := map[string]any{"command_key": deref(x.Step.CommandKey), "params": x.Step.Params}
		data, err := x.Engine.notRunRecently(ctx, x.Run, x.Step)
		if err != nil {
			return StepResult{Input: in}, err
		}
		passed, _ := data["passed"].(bool)
		return StepResult{Input: in, Data: data, Passed: passed}, nil
	}
	in := map[string]any{"worker_id": x.Step.WorkerID, "command_key": deref(x.Step.CommandKey)}
	worker, err := x.Engine.workerFor(ctx, x.Run, x.Step)
	if err != nil {
		return StepResult{Input: in}, err
	}
	in["worker_id"] = worker
	data, err := x.Engine.dispatch(ctx, x.Run, x.Step, worker, nil, x.Engine.t.Check)
	if err != nil {
		return StepResult{Input: in}, err
	}
	// The worker's verdict, as its PlaybookCheckResult — the only field acted on.
	passed, ok := data["passed"].(bool)
	if !ok {
		return StepResult{Input: in, Data: data}, fmt.Errorf("%s did not return a pass/fail verdict — is it a playbook check?", deref(x.Step.CommandKey))
	}
	return StepResult{Input: in, Data: data, Passed: passed}, nil
}

// ─── action ───────────────────────────────────────────────────────────────────

type actionStep struct{}

func (actionStep) Type() string                    { return StepAction }
func (actionStep) Phase() Phase                    { return PhaseAction }
func (actionStep) Budget(t Timeouts) time.Duration { return t.Action }

func (actionStep) Exec(ctx context.Context, x *StepContext) (StepResult, error) {
	params, err := x.Expander(ctx, false).Params(x.Step.Params)
	if err != nil {
		return StepResult{}, err
	}
	in := map[string]any{"worker_id": x.Step.WorkerID, "command_key": deref(x.Step.CommandKey), "params": params}
	worker, err := x.Engine.workerFor(ctx, x.Run, x.Step)
	if err != nil {
		return StepResult{Input: in}, err
	}
	in["worker_id"] = worker
	data, err := x.Engine.dispatch(ctx, x.Run, x.Step, worker, params, x.Engine.t.Action)
	return StepResult{Input: in, Data: data}, err
}

// ─── built-in check: not_run_recently ─────────────────────────────────────────

// CheckNotRunRecently is the engine's own check, run here rather than on a worker: has this
// playbook already succeeded on the device max_runs times within the last window_hours? It stops an
// automatic fix from looping by sending the repeat to a person instead (the check fails, nothing
// acts). A check step with no worker and this command_key.
const CheckNotRunRecently = "not_run_recently"

// Bounds on its settings: at least one run, and a window from an hour to 30 days.
const (
	notRunRecentlyMaxRunsLimit = 20
	notRunRecentlyWindowLimit  = 720
)

// IsBuiltInCheck reports whether a check step is evaluated by the engine rather than sent to a
// worker.
func IsBuiltInCheck(step *Step) bool {
	return step.StepType == StepCheck && step.WorkerID == nil && deref(step.CommandKey) == CheckNotRunRecently
}

// NotRunRecentlyParams validates the check's settings and returns them as stored.
func NotRunRecentlyParams(params map[string]string) (map[string]string, error) {
	if err := OnlyParams(params, "max_runs", "window_hours"); err != nil {
		return nil, err
	}
	maxRuns, err := strconv.Atoi(strings.TrimSpace(params["max_runs"]))
	if err != nil || maxRuns < 1 || maxRuns > notRunRecentlyMaxRunsLimit {
		return nil, validationErr(fmt.Sprintf("max_runs must be a whole number from 1 to %d", notRunRecentlyMaxRunsLimit))
	}
	window, err := strconv.Atoi(strings.TrimSpace(params["window_hours"]))
	if err != nil || window < 1 || window > notRunRecentlyWindowLimit {
		return nil, validationErr(fmt.Sprintf("window_hours must be a whole number from 1 to %d", notRunRecentlyWindowLimit))
	}
	return map[string]string{"max_runs": strconv.Itoa(maxRuns), "window_hours": strconv.Itoa(window)}, nil
}

// notRunRecently counts this playbook's successful runs on the run's device within the window,
// plus its other runs there still checking or running — two automatic runs at once must not both
// pass on a count neither has added to yet. At the cap, an automatic run fails the check; a
// person's run passes with the count in its reason — the cap is there to bring a person in, and one
// already is. The result carries runs, in_progress and last_run_at for later steps to reference.
func (e *Engine) notRunRecently(ctx context.Context, run *Run, step *Step) (map[string]any, error) {
	params, err := NotRunRecentlyParams(step.Params)
	if err != nil {
		return nil, fmt.Errorf("settings are invalid — edit the step: %w", err)
	}
	if run.PlaybookID == nil {
		return nil, fmt.Errorf("this run's playbook has been deleted")
	}
	maxRuns, _ := strconv.Atoi(params["max_runs"])
	window, _ := strconv.Atoi(params["window_hours"])

	prior, err := e.store.RecentRunsOnResource(ctx, run.OrganizationID, *run.PlaybookID, run.ID, run.ResourceID, time.Now().UTC().Add(-time.Duration(window)*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("could not read earlier runs: %w", err)
	}
	// Finished runs and ones still going both count: a run in progress is about to act too.
	succeeded, inProgress := 0, 0
	var last *time.Time
	for _, p := range prior {
		if p.Status != RunSucceeded {
			inProgress++
			continue
		}
		succeeded++
		if last == nil {
			last = p.CompletedAt
		}
	}

	data := map[string]any{"runs": succeeded, "in_progress": inProgress, "last_run_at": "—"}
	what := fmt.Sprintf("%s succeeded on %s %d× in the last %dh (cap %d)", run.PlaybookName, run.ResourceID, succeeded, window, maxRuns)
	if last != nil {
		data["last_run_at"] = last.UTC().Format("2 Jan 15:04 UTC")
		what += ", last at " + data["last_run_at"].(string)
	}
	if inProgress > 0 {
		what += fmt.Sprintf(", %d more running now", inProgress)
	}

	switch {
	case succeeded+inProgress < maxRuns:
		data["passed"], data["reason"] = true, what
	case run.CreatedBy != nil || run.ExecutedBy != nil:
		data["passed"], data["reason"] = true, what+" — at the cap, run anyway by a person"
	default:
		data["passed"], data["reason"] = false, what+" — it keeps coming back, so a person should look before it runs again"
	}
	return data, nil
}
