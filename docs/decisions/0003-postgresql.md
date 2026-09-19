# ADR-0003: PostgreSQL as the only state

## Status

Accepted.

## Context

Need durable relational state (devices now; sync/outbox/analytics later) with one operational story across Railway/Neon/Render/RDS/self-hosted.

## Decision

PostgreSQL 18 locally (pinned image). No Redis/brokers in Phase 1A; future queues use Postgres-backed outbox first.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
