package hxdna

import (
	"encoding/json"
	"time"
)

// HxCommand is the standard payload the control plane sends over NATS
// to instruct a worker to perform an action. See: EIP Command Message.
type HxCommand struct {
	CommandKey string            `json:"command_key"`
	RequestID  string            `json:"request_id"`
	TicketID   string            `json:"ticket_id"`
	ResourceID string            `json:"resource_id"`
	Params     map[string]string `json:"params,omitempty"`
	// ResourceContext carries optional pre-command enrichment data (e.g. a CMDB/topology
	// lookup) the control plane gathered before dispatching this command — raw JSON so
	// each worker decodes it into its own typed struct, same idiom as TriageResponse.Evidence.
	// Omitted entirely for workers/commands that don't use it — nothing about existing
	// unmarshaling changes when this field is absent.
	ResourceContext json.RawMessage `json:"resource_context,omitempty"`
}

// HxResult is the response a worker publishes back after executing an HxCommand.
type HxResult struct {
	RequestID  string          `json:"request_id"`
	Success    bool            `json:"success"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      string          `json:"error,omitempty"`
	ExecutedAt time.Time       `json:"executed_at"`
}

// ToJSON serialises the result for NATS publishing.
// If serialisation fails (e.g. Data holds an unmarshallable type), it returns
// a safe error result so the control plane always receives a valid response.
func (r HxResult) ToJSON() []byte {
	b, err := json.Marshal(r)
	if err != nil {
		fallback := HxResult{
			RequestID:  r.RequestID,
			Success:    false,
			Error:      "result serialisation failed: " + err.Error(),
			ExecutedAt: r.ExecutedAt,
		}
		b, _ = json.Marshal(fallback)
	}
	return b
}

// Handler is a deterministic command executor.
// It receives the full HxCommand and returns structured data or an error.
// Triage handlers may include deterministic classification logic (see TriageOutcome).
// Resolver/action handlers must remain side-effect-only executors.
type Handler func(cmd HxCommand) (any, error)

// TriageOutcome is the structured triage classification computed deterministically
// by the worker from its collected evidence. Workers include this in the triage
// command result so the control plane never asks an LLM to make rule-based decisions.
// Value is the classification string (e.g. "restart_service") — the same vocabulary
// declared by ContractEntry.PossibleOutcomes and matched against a named outcome
// downstream, so it is deliberately named "outcome" end to end rather than
// "decision"/"action" at some hops and "outcome" at others.
type TriageOutcome struct {
	Value    string `json:"value"`
	Severity string `json:"severity"`
	Trigger  string `json:"trigger"`
	Findings string `json:"findings"`
}

// TriageResponse is the Data payload returned by a triage command.
// Evidence is preserved as raw JSON so each side can decode it into its own typed struct.
type TriageResponse struct {
	Evidence json.RawMessage `json:"evidence"`
	Outcome  TriageOutcome   `json:"outcome"`
	// Metrics is optional — the observability numbers this triage worker considers worth
	// graphing, in a shape it defines itself (name, kind, value, labels). The control plane
	// never reads or interprets this; it only relays it, opaque, to whichever worker a
	// ServiceAgent has configured as its Metrics Worker (see
	// docs/architecture/observability-metrics.md in the docs repo). Only the worker that
	// produced Evidence in the first place is ever expected to know what's worth reporting
	// out of it — that's a domain judgment the control plane and every other worker must
	// stay out of, same reasoning as why Evidence itself is opaque JSON.
	Metrics []Metric `json:"metrics,omitempty"`
}

// MetricKind is the Prometheus metric type this value should be exposed as. Deliberately just
// counter/gauge — both single-value types Metric's one float64 Value can actually represent.
// A real Prometheus histogram needs bucket boundaries plus separate _sum/_count series, none of
// which fit this envelope; adding MetricKindHistogram here would be a constant with no valid
// implementation, not a real option. If a worker ever needs histogram-shaped data, that's a
// different, richer envelope to design when that real case shows up — not a value to pre-add now.
type MetricKind string

const (
	MetricKindCounter MetricKind = "counter"
	MetricKindGauge   MetricKind = "gauge"
)

// Metric is a single observability data point a worker wants graphed, in a transport
// envelope shared by every worker regardless of domain — deliberately NOT a schema of named
// domain fields (that was tried and rejected: it assumes every worker's numbers look alike,
// which they don't — a GPU count and a queue depth have nothing in common except that
// they're both "a number worth graphing"). Name/Kind/Labels are entirely the emitting
// worker's own choice; nothing here is centrally registered or validated by hxdna or the
// control plane.
type Metric struct {
	Name   string            `json:"name"`
	Kind   MetricKind        `json:"kind"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// InputSchema declares the parameters an action or command accepts, as
// param name → human-readable type/description. Shared by CommandMeta and
// ContractEntry so the two declarations cannot drift in shape.
type InputSchema map[string]string

// CommandMeta describes runtime metadata for a command so the control plane
// and resolver AI can reason about safety, ordering, and expected I/O.
type CommandMeta struct {
	Prerequisites []string          `json:"prerequisites,omitempty"`
	InputSchema   InputSchema       `json:"input_schema,omitempty"`
	OutputSchema  map[string]string `json:"output_schema,omitempty"`
	Idempotent    bool              `json:"idempotent"`
}

// Command is a single entry in a worker's capability manifest.
type Command struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	// Kind is "ask" for a read-only, side-effect-free command a human can invoke
	// directly on demand (e.g. a lookup) — the control plane only offers "ask"
	// commands in its free-form operator lookup surface. Everything else (empty,
	// or any other value) is excluded from that surface, whether it's a real
	// side-effecting action or an internal/system command like ping — Kind only
	// promises "safe to expose to a human ask box", it doesn't otherwise classify
	// the command. Mirrors MCP's resources (application/human-selected, read-only)
	// vs tools (side-effecting) split.
	Kind string      `json:"kind,omitempty"`
	Meta CommandMeta `json:"meta"`
}

// TriageOutcomeDescriptor documents one value a triage entry's command can return as its
// classification, paired with a human-readable explanation — the same Value+Description shape
// Command and ContractEntry already use, so an outcome is described the same way a command is
// rather than being a bare, unexplained string.
type TriageOutcomeDescriptor struct {
	Value       string `json:"value"`
	Description string `json:"description"`

	// EvidenceFields declares the dot-paths into this triage entry's evidence JSON that are
	// meaningful when this specific outcome fires, as path → human-readable description (same
	// shape as InputSchema, deliberately reusing that type rather than inventing a new one).
	// Evidence shape is otherwise opaque JSON the control plane cannot introspect — this is the
	// worker's own declaration of which fields are worth surfacing for this outcome (e.g. a
	// resolver step's schedule_job scheduled_at_from), scoped to the outcome rather than dumped
	// as one flat schema for the whole evidence blob, most of which is irrelevant to any given
	// outcome. Optional — omit for outcomes with no fields worth surfacing this way. Nothing
	// enforces these paths actually resolve against real evidence; keeping them accurate is the
	// worker author's responsibility, same as every other manifest declaration.
	EvidenceFields InputSchema `json:"evidence_fields,omitempty"`
}

// ContractEntry is a single action the worker exposes as a named, selectable
// operation. Triage entries describe evidence-collection commands; resolver entries
// describe executable actions the resolver can dispatch; lookup entries describe
// read-only actions eligible as a step in a saved Lookup chain.
type ContractEntry struct {
	ActionKey   string `json:"action_key"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`

	// InputSchema declares the parameters this action accepts (populated in
	// ResolverContract and LookupContract entries — triage entries take no
	// operator-supplied params). The control plane renders these as inputs on a
	// call_worker resolution step (or the first step of a Lookup chain) and forwards
	// operator-provided values via HxCommand.Params. Params are always optional at
	// the protocol level — the worker owns defaulting and validation of anything it
	// doesn't receive.
	InputSchema InputSchema `json:"input_schema,omitempty"`

	// OutputSchema declares the fields this action's result contains (populated in
	// ResolverContract and LookupContract entries, same idiom as CommandMeta.OutputSchema
	// for ask-kind commands). Lets the control plane offer a later step's input as a
	// reference to an earlier step's declared output field — e.g. auto-wiring a canonical
	// field like "resource_id" forward without a human having to already know it's there.
	OutputSchema InputSchema `json:"output_schema,omitempty"`

	// PossibleOutcomes is the full set of outcomes this triage entry's command can return
	// (populated in TriageContract entries only — resolver entries execute actions and have no
	// outcome output). Each worker type declares its own vocabulary here, with a description
	// per outcome; the control plane uses the Value to offer a real choice of named outcomes
	// instead of free text that has to coincidentally match whatever string the worker
	// eventually returns.
	PossibleOutcomes []TriageOutcomeDescriptor `json:"possible_outcomes,omitempty"`
}

// Manifest is published to the control plane at enrollment and re-announced on every NATS reconnect.
type Manifest struct {
	WorkerType       string          `json:"worker_type"`
	Version          string          `json:"version"`
	Commands         []Command       `json:"commands"`
	TriageContract   []ContractEntry `json:"triage_contract,omitempty"`
	ResolverContract []ContractEntry `json:"resolver_contract,omitempty"`
	// LookupContract declares the actions this worker exposes as eligible steps in a saved
	// Lookup chain — same self-contained shape as ResolverContract (own InputSchema/
	// OutputSchema; ActionKey must still match a real registered handler key, since dispatch
	// is routed by that string — same as ResolverContract, Commands membership is not
	// required, but the declared metadata here is authoritative either way, not the
	// matching Command's Meta if one happens to exist). Deliberately a separate opt-in from
	// Commands' Kind == "ask": that marks a command safe for the operator's own free-form
	// Ask box in the moment, while LookupContract marks it additionally safe to be wired
	// into a saved, unattended chain a human configures once and never revisits. A worker
	// can have ask commands with no LookupContract entry (Ask-only) or vice versa, though in
	// practice most ask commands will want both.
	LookupContract []ContractEntry `json:"lookup_contract,omitempty"`
	// AlertContract declares this worker's default recheck policy for the alert->incident
	// pipeline — nil (and omitted from JSON) for workers that don't publish alerts at all.
	// The control plane does NOT override RecheckIntervalSeconds/MaxRetries — they are the
	// sole terms of the control plane<->worker relationship, declared solely by the worker;
	// an incident cannot be raised without a valid contract. One value for the whole worker,
	// not per alert/check type — matches today's reality (e.g. a monitoring source polling
	// everything on one cadence). A pointer, not a value, specifically so omitempty works:
	// encoding/json never treats a struct value as "empty", so a value field would always
	// serialize as zeros even when a worker never set it.
	AlertContract *AlertPolicy `json:"alert_contract,omitempty"`

	// EnrichCommandKey names the one Command (must exist in Commands, and should be
	// Kind == "ask" for the same read-only/side-effect-free guarantee) this worker wants
	// used for automated pre-triage enrichment — the control plane calls it with no human
	// present to disambiguate, so it needs one unambiguous answer, not a menu. Declared
	// solely by the worker, same as AlertContract: the worker is the only one who knows
	// which of its ask-safe commands is the right default for unattended use versus one
	// meant for a human to invoke deliberately (e.g. a general lookup vs a specialised
	// trace). Omitted (empty string) for a worker with nothing worth auto-enriching with,
	// or exactly one ask command declared, where there's nothing to disambiguate.
	EnrichCommandKey string `json:"enrich_command_key,omitempty"`

	// MetricsCommandKey names the one Command (must exist in Commands) this worker wants
	// used for automated post-triage metrics push — the control plane calls it with no
	// human present, right after a triage completes, so it needs one unambiguous answer,
	// not a menu. Same shape and reasoning as EnrichCommandKey, but for the opposite
	// direction of data flow (control plane -> worker -> external system, not worker ->
	// control plane): this command is a real side-effecting write (pushes data to an
	// observability backend), so unlike EnrichCommandKey it should NOT be Kind == "ask" —
	// "ask" specifically promises read-only/side-effect-free and is what exposes a command
	// in the operator's free-form ask box, neither of which applies here. Omitted (empty
	// string) for a worker with no metrics-push capability.
	MetricsCommandKey string `json:"metrics_command_key,omitempty"`
}

// AlertCard is the payload a worker publishes on hx.agents.{org}.{worker}.alert to
// self-initiate the alert->incident pipeline. Org/worker identity is deliberately not a
// field here — the control plane takes it from the NATS subject instead, so a worker
// can't spoof another org/worker by what it puts in the JSON body.
type AlertCard struct {
	ResourceID string `json:"resource_id"`
	Source     string `json:"source"`   // e.g. "monitoring-system"
	Severity   string `json:"severity"` // low | medium | high | critical
	Message    string `json:"message"`
	AlertID    string `json:"alert_id"` // source's own stable ID — the worker republishes every still-failing alert on every poll tagged with this, so the control plane is responsible for rejecting repeats.

	// ResourceType is the source's own entity/asset-class label for what ResourceID
	// refers to (e.g. a monitoring source's "device"/"port"/"sensor"). Informational only,
	// like FirstFailed/FailCount below — nothing in the control plane branches on it today.
	// Omitted if the source doesn't distinguish resource types.
	ResourceType string `json:"resource_type,omitempty"`

	// FirstFailed is when the SOURCE says this alert started failing (e.g. a monitoring
	// source's last_changed field), not when the control plane first heard about it — those can diverge
	// if a worker was disconnected, the control plane restarted, or ingestion lagged.
	// ISO8601, omitted if the source doesn't track it. Provenance only — does not drive
	// the control plane's own recheck/retry state machine, which times itself
	// independently against AlertContract.
	FirstFailed string `json:"first_failed,omitempty"`

	// FailCount is the source's own consecutive-failure count for this alert (a.k.a.
	// flap/occurrence count) — how long this has been broken by the source's own
	// measure, independent of the control plane's own retry counting after the incident
	// was raised. Omitted if the source doesn't track it.
	FailCount int `json:"fail_count,omitempty"`

	// Properties holds resource_type-specific enrichment — its content depends on
	// ResourceType, same slot for every type rather than a new field per type (mirrors
	// ARM's properties-varies-by-type convention). Only device fields exist today;
	// nil when the source has nothing to report for this resource type. If a second
	// resource type needs its own enrichment later, this should become json.RawMessage
	// decoded per resource_type rather than growing ResourceProperties to cover every
	// type in one struct.
	Properties *ResourceProperties `json:"properties,omitempty"`
}

// ResourceProperties is device-specific enrichment (ResourceType == "device" today —
// "device" here means any monitored node: server, switch, router, host, not network
// hardware specifically). Not applicable to port/sensor alerts, which carry
// no Properties at all rather than an empty/zero-valued one.
type ResourceProperties struct {
	FQDN     string `json:"fqdn,omitempty"`
	IP       string `json:"ip,omitempty"`
	Vendor   string `json:"vendor,omitempty"`
	Hardware string `json:"hardware,omitempty"`
	Location string `json:"location,omitempty"`
}

// AlertPolicy is the recheck policy a worker declares for the alerts it publishes —
// how long the control plane should wait between rechecks and how many rechecks to
// attempt before an unrecovered alert becomes an incident. See Manifest.AlertContract.
// Description is the worker's own explanation of these numbers (why this cadence, what
// it's checking) — same (value, description) shape as Command and
// TriageOutcomeDescriptor, so the control plane never has to invent its own text for
// this contract the way it does for every other one.
type AlertPolicy struct {
	RecheckIntervalSeconds int    `json:"recheck_interval_seconds"`
	MaxRetries             int    `json:"max_retries"`
	Description            string `json:"description,omitempty"`
}

// AlertRecheckResult is the Data payload returned by an "alert_recheck" command — the
// control plane's active follow-up check on a still-open incident. Cleared true means
// the worker no longer sees this alert_id as failing.
type AlertRecheckResult struct {
	Cleared bool `json:"cleared"`
}
