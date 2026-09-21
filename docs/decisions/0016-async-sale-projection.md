# ADR-0016: Durable inbox + asynchronous PostgreSQL projection

## Status

Accepted.

## Context

The first business event (sale.finalized.v1) needs dashboard/reporting
query shapes without coupling transport reliability to analytics
completeness. Making the desktop ACK wait for projection would tie sync
success to projector health, schema evolution, and processing latency.

## Decision

HTTP ingestion commits only the immutable event to `sync_events` and ACKs.
A PostgreSQL-backed in-process projector (wake + periodic durable scan,
row-level claiming, no broker) derives `sales_projection` and children
asynchronously. `sync_event_processing(event_id, processor)` tracks
pending/retry/blocked/processed with bounded backoff; logical sale_id
conflicts become visible `blocked` state, never silent overwrites.

## Consequences

Transport reliability (at-least-once + idempotent ACK) is isolated from
projection failures; restarts recover from the inbox without desktop
resends; future processors (returns, products) reuse the same mechanism;
projection tables are rebuildable derived state. Documented in
docs/sync/protocol.md and docs/operations/projection.md; revisited only by
a new ADR.
