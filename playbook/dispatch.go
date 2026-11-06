package playbook

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ivedhahpc/hxdna"
)

// Dispatcher is how the app reaches its workers.
type Dispatcher interface {
	// Entry returns the worker's current PlaybookContract entry for key — from Checks when
	// stepType is StepCheck, else from Actions. An error when the worker isn't the organization's,
	// has no manifest, or doesn't offer key (FindEntry builds that error from a manifest).
	// Read again before every send, not only when the step was saved: a worker that has withdrawn
	// a command must not have it run anyway.
	Entry(ctx context.Context, orgID, workerID uuid.UUID, stepType, key string) (*hxdna.ContractEntry, error)
	// Send sends cmd to the worker and waits up to timeout for its HxResult, returning the result's
	// Data as an object. A worker that answers Success false is an error with its message.
	// run is the run the command belongs to (an app may read run.Ext, e.g. to set cmd.TicketID).
	Send(ctx context.Context, run *Run, workerID uuid.UUID, cmd hxdna.HxCommand, timeout time.Duration) (map[string]any, error)
}

// WorkerResolver is optional: a Dispatcher implements it when its check and action steps may leave
// WorkerID empty and the worker comes from the run instead — Provision, for example, sends a step to
// the Foreman worker of the run's site (from run.Ext), so one playbook serves every site. A step
// that names a worker always goes to that worker. Without a resolver, a step with no worker fails.
//
// WorkerFor is asked again for every step, like the manifest: if the site's worker changes between
// a person's checks and Run, the actions go to the new one (re-checked against its own manifest).
// An error or uuid.Nil fails the step before anything is sent.
//
// A check with no worker and command_key not_run_recently is still the engine's built-in check, so
// an app must not let a worker command take that name.
type WorkerResolver interface {
	WorkerFor(ctx context.Context, run *Run, step *Step) (uuid.UUID, error)
}

// FindEntry finds key in manifest's PlaybookContract list for stepType (Checks for a check, Actions
// otherwise). workerName is only for the error message.
func FindEntry(manifest hxdna.Manifest, workerName, stepType, key string) (*hxdna.ContractEntry, error) {
	var list []hxdna.ContractEntry
	kind := "action"
	if pc := manifest.PlaybookContract; pc != nil {
		list = pc.Actions
		if stepType == StepCheck {
			list, kind = pc.Checks, "check"
		}
	}
	for i := range list {
		if list[i].ActionKey == key {
			return &list[i], nil
		}
	}
	return nil, validationErr(fmt.Sprintf("worker %s does not offer %q as a playbook %s", workerName, key, kind))
}

// workerFor is the worker a check or action step goes to: the one it names, else the one the
// Dispatcher's WorkerResolver picks for this run.
func (e *Engine) workerFor(ctx context.Context, run *Run, step *Step) (uuid.UUID, error) {
	if step.WorkerID != nil {
		return *step.WorkerID, nil
	}
	r, ok := e.dispatcher.(WorkerResolver)
	if !ok {
		return uuid.Nil, fmt.Errorf("step has no worker")
	}
	id, err := r.WorkerFor(ctx, run, step)
	if err != nil {
		return uuid.Nil, err
	}
	if id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("no worker to run %s on %s", deref(step.CommandKey), run.ResourceID)
	}
	return id, nil
}

// dispatch sends a check or action step's command to workerID.
func (e *Engine) dispatch(ctx context.Context, run *Run, step *Step, workerID uuid.UUID, params map[string]string, timeout time.Duration) (map[string]any, error) {
	if step.CommandKey == nil {
		return nil, fmt.Errorf("step has no command")
	}
	if _, err := e.dispatcher.Entry(ctx, run.OrganizationID, workerID, step.StepType, *step.CommandKey); err != nil {
		return nil, err
	}
	cmd := hxdna.HxCommand{
		CommandKey: *step.CommandKey,
		RequestID:  uuid.New().String(),
		ResourceID: run.ResourceID,
		Params:     params,
	}
	return e.dispatcher.Send(ctx, run, workerID, cmd, timeout)
}
