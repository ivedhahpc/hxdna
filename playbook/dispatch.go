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

// dispatch sends a check or action step's command to its worker.
func (e *Engine) dispatch(ctx context.Context, run *Run, step *Step, params map[string]string, timeout time.Duration) (map[string]any, error) {
	if step.WorkerID == nil || step.CommandKey == nil {
		return nil, fmt.Errorf("step has no worker or command")
	}
	if _, err := e.dispatcher.Entry(ctx, run.OrganizationID, *step.WorkerID, step.StepType, *step.CommandKey); err != nil {
		return nil, err
	}
	cmd := hxdna.HxCommand{
		CommandKey: *step.CommandKey,
		RequestID:  uuid.New().String(),
		ResourceID: run.ResourceID,
		Params:     params,
	}
	return e.dispatcher.Send(ctx, run, *step.WorkerID, cmd, timeout)
}
