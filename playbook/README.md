# hxdna/playbook

The playbook engine shared by HelixOps and the apps (Provision first). Its own Go module inside
the hxdna repo, so workers — which import `hxdna` — never pull it in, and hxdna's version keeps
meaning "the worker protocol changed".

```
go get github.com/ivedhahpc/hxdna/playbook@v0.2.0
```

(Tagged `playbook/v0.x.y` in the hxdna repo.) Plan and reasoning:
`docs/architecture/playbook-engine-extraction.md` in the hive docs repo.

## What it does

A playbook is ordered steps run against one device: **check steps first, then — only if every
check passed — action steps.** The engine guarantees:

- **One run acts on a device at a time** (per organization, case-insensitive). A run whose device
  was changed by another run after its checks started is refused, not queued.
- **A person's checks go stale:** after 15 min, or once the playbook is edited, Run is refused.
- **Nothing is cut off halfway:** each phase must fit its time window before it starts.
- **A withdrawn command never runs:** the worker's manifest is re-read before every send.
- **Every step attempted is recorded** with what it was given and what came back.
- **Interrupted runs fail** (backend restart) instead of holding the device.

## Using it in an app

```go
eng := playbook.New(store, dispatcher, playbook.Options{
    Hooks: playbook.Hooks{RunByName: users.DisplayName},
})
eng.Register(myStepType{}) // the app's own step types, if any

// A person: check, then run.
run, err := eng.StartChecks(ctx, playbook.StartRequest{OrgID: org, PlaybookID: id, ResourceID: "node1", By: &userID})
// … poll eng.Detail until ready, then:
run, err = eng.Execute(ctx, org, run.ID, userID)

// Automation, or a request from HelixOps: start to finish in one call.
run, results, err := eng.RunNow(ctx, playbook.StartRequest{OrgID: org, PlaybookID: id, ResourceID: "node1"})
```

The app provides:

| | |
|---|---|
| `Store` | The four tables in its own database — copy `schema.sql` (`playbook.Schema`) into its migrations and add its foreign keys. `ClaimAction` must follow `ActionClaim`'s rules; the unique index in `schema.sql` enforces the core one. `GetRun` fills `Ext` and `ReasonFallback` if the app uses them. |
| `Dispatcher` | How it reaches its workers (its NATS subjects) and reads their manifests — `FindEntry` does the manifest lookup. Optionally also a `WorkerResolver`: then a check or action step may leave its worker empty and `WorkerFor` picks one per run (Provision: the Foreman worker of the run's site, so one playbook serves every site). A step that names a worker always goes there. |
| Step types | Optional. `check`, `action` and the built-in `not_run_recently` check come with the engine. |
| `Hooks` | Optional. Extra `{{placeholders}}`, who ran it, extra step-order rules, before/after the actions. |

Playbook and step editing stays in the app (its own API); it uses the same rules the engine runs
by: `ValidateStepName`, `Engine.ValidateOrder`, `ValidateActionParams` with `Dispatcher.Entry`,
`OnlyParams`, `NotRunRecentlyParams`, `RenameStepRefs`.

Errors: `IsValidation(err)` is the asker's mistake (answer 400); `ErrNotFound`, `ErrRunNotReady`,
`ErrChecksStale`, `ErrChecksFailed`, `ErrResourceBusy` are sentinels (`errors.Is`).

## Placeholders

`{{resource_id}}`, `{{run_by}}`, `{{reason}}` (falls back to `Run.ReasonFallback`), and
`{{step_name.dot.path}}` into an earlier step's result. Anything else goes to `Hooks.Placeholder`.

## Timeouts

Defaults are HelixOps's: check 150 s, action 60 s, 10 min per phase of a person's run, checks valid
15 min, a run untouched 11 min counts as interrupted. `StaleAfter` is kept above `Run`, and
`RunNow` refuses a playbook whose worst case is longer than `StaleAfter` — otherwise another run
could take the device while it is still acting.

## Schema versions

| Version | Change |
|---|---|
| 1 | First: `playbooks`, `playbook_steps`, `playbook_runs`, `playbook_run_steps` as HelixOps has them, without its ticket columns (`sor_config_id`, `agent_rule_log_id`, `created_ticket_*`, `offer_in_chat`, `require_typed_confirm`) and without a `step_type` CHECK (apps register their own types). |

## Versions

| Version | Change |
|---|---|
| v0.1.0 | First: the engine from HelixOps. |
| v0.2.0 | `WorkerResolver` (optional on the Dispatcher): steps with no worker take the run's. Additive; schema unchanged. |
