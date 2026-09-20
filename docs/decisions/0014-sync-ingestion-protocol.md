# ADR-0014: Sync ingestion: batched, atomic, idempotent

## Status

Accepted.

## Context

Desktop outbox drains need efficient multi-event sends with clear retry semantics; partial commits would make client recovery ambiguous.

## Decision

POST /api/v1/sync/batches only; all-or-nothing commit; event_id PK (globally unique) + canonical-payload SHA-256; identical retry → already_accepted; same-ID-different-content → 409 EVENT_ID_REUSE; intra-batch duplicate IDs → 400; limits 100 events / 64 KiB payload / 8 MiB body; ACK means durable commit only. No sync_batches table; batch_id is an optional echoed client UUID.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
