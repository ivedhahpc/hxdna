package playbook

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Store is the app's persistence for the engine: the tables in schema.sql, in the app's own
// database. Every read is scoped to the organization. A Get that finds nothing returns (nil, nil).
//
// An app's Store may fill Ext on what it returns (its own columns, a linked ticket…) and read Ext
// on what it is given; the engine passes it through untouched.
type Store interface {
	GetPlaybook(ctx context.Context, orgID, playbookID uuid.UUID) (*Playbook, error)
	// ListSteps returns the playbook's steps in order (order_index, then created_at).
	ListSteps(ctx context.Context, orgID, playbookID uuid.UUID) ([]*Step, error)

	CreateRun(ctx context.Context, run *Run) error
	// GetRun also fills what isn't stored but the run needs to act: Ext and ReasonFallback (an app
	// that uses them) — Execute runs the actions on the run it reads here.
	GetRun(ctx context.Context, orgID, runID uuid.UUID) (*Run, error)
	// SetRunStatus moves the run to status with errMsg as its error ("" clears it), touching
	// updated_at, and completed_at too when completed.
	SetRunStatus(ctx context.Context, runID uuid.UUID, status, errMsg string, completed bool) error
	// ClaimAction moves a run into running — its action phase — which holds its resource. See
	// ActionClaim for the rules; schema.sql's idx_playbook_runs_acting_resource enforces the core one.
	ClaimAction(ctx context.Context, c ActionClaim) (claimed bool, holder *Run, err error)
	// ActingOn returns the run holding orgID's resourceID (case-insensitive) — running and touched
	// since staleBefore — or nil. A quick look before starting work; ClaimAction decides.
	ActingOn(ctx context.Context, orgID uuid.UUID, resourceID string, staleBefore time.Time) (*Run, error)
	// FailIfStale moves a run still in inProgress, untouched since before cutoff, to failStatus with
	// errMsg; completed_at becomes its last sign of life (updated_at), not now. Reports whether it
	// changed the run.
	FailIfStale(ctx context.Context, runID uuid.UUID, inProgress, failStatus, errMsg string, cutoff time.Time) (bool, error)
	// RecentRunsOnResource returns playbookID's runs on resourceID (case-insensitive), other than
	// excludeRunID, that succeeded (completed_at) since since or are still checking or running —
	// in-progress first, then finished newest first. A run left ready waits on a person and isn't
	// included. What the built-in not_run_recently check counts.
	RecentRunsOnResource(ctx context.Context, orgID, playbookID, excludeRunID uuid.UUID, resourceID string, since time.Time) ([]*Run, error)

	CreateRunStep(ctx context.Context, step *RunStep) error
	// ListRunSteps returns a run's attempted steps in the order they ran.
	ListRunSteps(ctx context.Context, runID uuid.UUID) ([]*RunStep, error)
}

// ActionClaim is what a run asks for to start its action phase.
//
// ClaimAction moves the run from From to running only when:
//   - it is still in From (and, with CheckedAfter, its updated_at — when its checks finished — is
//     not before CheckedAfter);
//   - no other run of the organization is running on the same resource (case-insensitive);
//   - no other run on the resource succeeded or failed after this run was created — its checks
//     would no longer describe the resource.
//
// Before deciding, a run still running on the resource but untouched since StaleBefore is failed
// with InterruptedMsg (completed_at = its updated_at), so a crash never leaves a resource held.
//
// claimed is false when the run can't start: holder is the run in the way (the one running now
// first, else the latest that acted since this run's checks), or nil when the run itself wasn't in
// From or its checks are older than CheckedAfter.
type ActionClaim struct {
	RunID, OrgID uuid.UUID
	// From is the status the run must still be in: checking (a run straight from its checks) or
	// ready (a person's Run after checking).
	From string
	// ExecutedBy is recorded on the run; nil keeps what it has.
	ExecutedBy *uuid.UUID
	// CheckedAfter, when set, refuses a ready run whose checks finished before it.
	CheckedAfter *time.Time
	// StaleBefore: a run still running but untouched since before this was interrupted.
	StaleBefore time.Time
}
