package playbook

import _ "embed"

// SchemaVersion is schema.sql's version. An app records which version its migrations match.
const SchemaVersion = 1

// Schema is the engine's tables as PostgreSQL DDL (schema.sql), for an app's first migration.
//
//go:embed schema.sql
var Schema string
