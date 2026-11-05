-- Playbook engine tables — schema version 1 (hxdna/playbook).
--
-- Each app copies this into its own migrations, then adds its own foreign keys and columns:
--   organization_id      -> its organizations table   (ON DELETE CASCADE)
--   created_by / executed_by -> its users table       (ON DELETE SET NULL)
--   playbook_steps.worker_id -> its workers table     (ON DELETE CASCADE)
-- and any extra columns its own step types need (HelixOps: sor_config_id, agent_rule_log_id,
-- created_ticket_*, offer_in_chat …), which its Store reads into Ext.
--
-- step_type has no CHECK: an app registers its own step types, and the engine rejects an unknown
-- one (Engine.ValidateOrder). A change here bumps the version above and is listed in the README.

CREATE TABLE IF NOT EXISTS playbooks (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    internal_id      SERIAL UNIQUE,
    organization_id  UUID         NOT NULL,
    name             VARCHAR(255) NOT NULL,
    description      TEXT,
    ask_reason       BOOLEAN      NOT NULL DEFAULT false,
    created_by       UUID,
    created_at       TIMESTAMPTZ  DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(organization_id, name)
);
CREATE INDEX IF NOT EXISTS idx_playbooks_internal_id ON playbooks(internal_id);
CREATE INDEX IF NOT EXISTS idx_playbooks_organization_id ON playbooks(organization_id);

CREATE TABLE IF NOT EXISTS playbook_steps (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    internal_id      SERIAL UNIQUE,
    organization_id  UUID         NOT NULL,
    playbook_id      UUID         NOT NULL REFERENCES playbooks(id) ON DELETE CASCADE,
    step_name        VARCHAR(100) NOT NULL,
    order_index      INTEGER      NOT NULL DEFAULT 0,
    step_type        VARCHAR(20)  NOT NULL,
    worker_id        UUID,
    command_key      VARCHAR(100),
    params           JSONB,
    content          TEXT,
    created_at       TIMESTAMPTZ  DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(playbook_id, step_name)
);
CREATE INDEX IF NOT EXISTS idx_playbook_steps_internal_id ON playbook_steps(internal_id);
CREATE INDEX IF NOT EXISTS idx_playbook_steps_organization_id ON playbook_steps(organization_id);
CREATE INDEX IF NOT EXISTS idx_playbook_steps_playbook_id ON playbook_steps(playbook_id);

CREATE TABLE IF NOT EXISTS playbook_runs (
    id                 UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    UUID         NOT NULL,
    playbook_id        UUID         REFERENCES playbooks(id) ON DELETE SET NULL,
    playbook_name      VARCHAR(255) NOT NULL,
    resource_id        VARCHAR(255) NOT NULL,
    status             VARCHAR(20)  NOT NULL CHECK (status IN ('checking', 'check_failed', 'ready', 'running', 'succeeded', 'failed')),
    error_message      TEXT,
    created_by         UUID,
    executed_by        UUID,
    reason             TEXT,
    completed_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ  DEFAULT NOW(),
    updated_at         TIMESTAMPTZ  DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_playbook_runs_organization_id ON playbook_runs(organization_id);
CREATE INDEX IF NOT EXISTS idx_playbook_runs_playbook_id ON playbook_runs(playbook_id);
-- One run per organization and resource (case-insensitive) in its action phase at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_playbook_runs_acting_resource
    ON playbook_runs (organization_id, LOWER(resource_id)) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_playbook_runs_organization_id_resource_id
    ON playbook_runs (organization_id, LOWER(resource_id), completed_at);

CREATE TABLE IF NOT EXISTS playbook_run_steps (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    playbook_run_id   UUID         NOT NULL REFERENCES playbook_runs(id) ON DELETE CASCADE,
    order_index       INTEGER      NOT NULL DEFAULT 0,
    step_name         VARCHAR(100) NOT NULL,
    step_type         VARCHAR(20)  NOT NULL,
    status            VARCHAR(20)  NOT NULL CHECK (status IN ('success', 'failed', 'skipped')),
    passed            BOOLEAN,
    input_json        JSONB,
    output_json       JSONB,
    error_message     TEXT,
    started_at        TIMESTAMPTZ  NOT NULL,
    completed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ  DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_playbook_run_steps_playbook_run_id ON playbook_run_steps(playbook_run_id);
