package playbook

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Step types the engine runs itself. An app adds its own with Engine.Register.
const (
	// StepCheck sends one of a worker's PlaybookContract checks (or runs the built-in
	// not_run_recently check) and stops the run when it doesn't pass.
	StepCheck = "check"
	// StepAction sends one of a worker's PlaybookContract actions with the step's params.
	StepAction = "action"
)

// Run statuses: checks first (checking -> ready | check_failed), then the actions and later
// steps (running -> succeeded | failed).
const (
	RunChecking    = "checking"
	RunCheckFailed = "check_failed"
	RunReady       = "ready"
	RunRunning     = "running"
	RunSucceeded   = "succeeded"
	RunFailed      = "failed"
)

// Run step statuses.
const (
	StepSucceeded = "success"
	StepFailed    = "failed"
	StepSkipped   = "skipped"
)

// InterruptedMsg is the error on a run whose goroutine went away mid-run (the backend restarted).
const InterruptedMsg = "interrupted — the run stopped without finishing (backend restarted?); check the device before running again"

// Playbook is a saved, ordered set of steps run against one device at a time.
type Playbook struct {
	ID             uuid.UUID `json:"id" db:"id"`
	OrganizationID uuid.UUID `json:"organization_id" db:"organization_id"`
	Name           string    `json:"name" db:"name"`
	Description    *string   `json:"description,omitempty" db:"description"`
	// AskReason makes a person running it give a reason, which fills {{reason}} in its steps.
	AskReason bool       `json:"ask_reason" db:"ask_reason"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty" db:"created_by"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt time.Time  `json:"updated_at" db:"updated_at"`
	// Ext is the app's own data on the playbook (its extra columns). The engine never reads it.
	Ext any `json:"-" db:"-"`
}

// Step is one ordered step of a Playbook. check/action run CommandKey — one of WorkerID's
// PlaybookContract checks/actions — against the run's device; an action also sends Params.
// WorkerID may be empty when the Dispatcher is a WorkerResolver: the run then decides the worker.
type Step struct {
	ID             uuid.UUID         `json:"id" db:"id"`
	OrganizationID uuid.UUID         `json:"organization_id" db:"organization_id"`
	PlaybookID     uuid.UUID         `json:"playbook_id" db:"playbook_id"`
	StepName       string            `json:"step_name" db:"step_name"`
	OrderIndex     int               `json:"order_index" db:"order_index"`
	StepType       string            `json:"step_type" db:"step_type"`
	WorkerID       *uuid.UUID        `json:"worker_id,omitempty" db:"worker_id"`
	CommandKey     *string           `json:"command_key,omitempty" db:"command_key"`
	Params         map[string]string `json:"params,omitempty" db:"params"`
	Content        *string           `json:"content,omitempty" db:"content"`
	CreatedAt      time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at" db:"updated_at"`
	// Ext is the app's own data on the step (its extra columns). The engine never reads it.
	Ext any `json:"-" db:"-"`
}

// Run is one run of a Playbook against one device. PlaybookName and ResourceID are snapshots so the
// record survives the Playbook being edited or deleted.
type Run struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	OrganizationID uuid.UUID  `json:"organization_id" db:"organization_id"`
	PlaybookID     *uuid.UUID `json:"playbook_id,omitempty" db:"playbook_id"`
	PlaybookName   string     `json:"playbook_name" db:"playbook_name"`
	ResourceID     string     `json:"resource_id" db:"resource_id"`
	Status         string     `json:"status" db:"status"`
	ErrorMessage   *string    `json:"error_message,omitempty" db:"error_message"`
	// CreatedBy is the person who started it, ExecutedBy the one who ran its actions; both nil
	// when automation ran it.
	CreatedBy  *uuid.UUID `json:"created_by,omitempty" db:"created_by"`
	ExecutedBy *uuid.UUID `json:"executed_by,omitempty" db:"executed_by"`
	// Reason is why the person ran it, when the playbook asks for one — what {{reason}} reads.
	Reason      *string    `json:"reason,omitempty" db:"reason"`
	CompletedAt *time.Time `json:"completed_at,omitempty" db:"completed_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at" db:"updated_at"`

	// Not stored:

	// ReasonFallback is what {{reason}} reads when the run has no Reason (HelixOps: the ticket's
	// triage outcome). Set by the app on StartRequest; not stored, so an app that uses it fills it
	// again in its Store's GetRun — Execute reloads the run before the actions.
	ReasonFallback string `json:"-" db:"-"`
	// RunByName is what {{run_by}} reads. Set by the engine as each phase starts.
	RunByName string `json:"-" db:"-"`
	// Ext is the app's own data on the run (its extra columns, the ticket it is linked to…).
	// The engine never reads it; it hands it back to the app's steps, hooks and dispatcher.
	Ext any `json:"-" db:"-"`
}

// RunStep is one step actually attempted in a run. Passed is set on check steps only.
type RunStep struct {
	ID           uuid.UUID        `json:"id" db:"id"`
	RunID        uuid.UUID        `json:"playbook_run_id" db:"playbook_run_id"`
	OrderIndex   int              `json:"order_index" db:"order_index"`
	StepName     string           `json:"step_name" db:"step_name"`
	StepType     string           `json:"step_type" db:"step_type"`
	Status       string           `json:"status" db:"status"`
	Passed       *bool            `json:"passed,omitempty" db:"passed"`
	InputJSON    *json.RawMessage `json:"input_json,omitempty" db:"input_json"`
	OutputJSON   *json.RawMessage `json:"output_json,omitempty" db:"output_json"`
	ErrorMessage *string          `json:"error_message,omitempty" db:"error_message"`
	StartedAt    time.Time        `json:"started_at" db:"started_at"`
	CompletedAt  *time.Time       `json:"completed_at,omitempty" db:"completed_at"`
	CreatedAt    time.Time        `json:"created_at" db:"created_at"`
}
