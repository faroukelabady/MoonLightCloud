# ADR-0012: Explicit migrations, verified schema at startup

## Status

Accepted.

## Context

Silent auto-migrate on boot risks split-brain deploys and hides operator intent; but running unmigrated code against old schema corrupts data.

## Decision

Migrations run explicitly (migrate.sh / CLI / serve --migrate opt-in). Startup verifies goose version == TargetVersion and fails fast otherwise. Readiness re-checks cheaply (table presence + ping).

## Consequences

Documented in the linked guides; revisited only by a new ADR.
