// Package playbook is the playbook engine shared by HelixOps and the apps (Provision, …):
// ordered steps run against one device at a time — check steps first, then, only if every check
// passed, action steps. One run per device acts at a time, every step attempted is recorded, and
// each phase must fit its time budget so a run is never cut off halfway through changing a device.
//
// The engine owns the run. The app supplies what is its own:
//
//   - Store — the four tables (playbooks, playbook_steps, playbook_runs, playbook_run_steps) in
//     the app's database, always scoped to an organization. schema.sql is the shared definition.
//   - Dispatcher — how the app reaches its workers (its NATS subjects) and reads their manifests.
//   - Step types — check and action (worker steps) and the built-in not_run_recently check come
//     with the engine; an app registers its own (HelixOps: ticket, bucket, chat, … steps).
//   - Hooks — extra {{placeholders}}, who ran it, extra step-order rules, what happens when the
//     actions succeed.
//
// Playbook and step editing (the admin API) stays in each app; the package gives it the same rules
// the engine runs by: ValidateStepName, Engine.ValidateOrder, RenameStepRefs, OnlyParams,
// NotRunRecentlyParams, FindEntry.
package playbook
