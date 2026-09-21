# ADR-0018 — Durable Sale Ownership

Date: 2026-09-21
Status: accepted
Scope: Phase 2E P2D-HIGH-02

## Problem

Multiple accepted event IDs can refer to one `sale_id` (desktop double
submission with regenerated IDs). The live projector arbitrated correctly,
but ownership lived only in disposable `sales_projection` rows: clearing
derived projections for a rebuild could elect a different winner and
rewrite financial history.

## Decision

Persist the logical winner outside disposable state in
`sale_event_ownership(sale_id PK, winning_event_id UNIQUE FK, winning_device_id, decided_at)`:

- `sync_events` — immutable accepted event history (never deleted).
- `sale_event_ownership` — durable conflict-arbitration decision (never
  deleted on rebuild).
- `sales_projection` + children + processing rows — disposable, rebuildable
  derived state.

Arbitration runs in its own transaction before any projection row exists
(`INSERT ... ON CONFLICT DO NOTHING RETURNING`; losers read the winner
under row lock). The decision is permanent at commit: a first owner's later
projection failure never transfers ownership — late events become
`SALE_ID_CONFLICT` while the owner retries. The projection transaction
re-verifies the winner at the write boundary; only the winner populates
Sale tables. Existing projections backfill ownership from the projected
`source_event_id` (migration 00007, failing loudly on inconsistency).

## Alternatives rejected

- `received_at` ordering only: ties and clock skew make it nondeterministic.
- Processing order: scheduling is not business history.
- Projection-table ownership only: disposable state cannot anchor rebuilds.

## Consequences

Rebuilds always reproduce the historical winner (tested with reversed order
and two projector instances). Downgrade refuses while decisions exist.
Integrity query (`OwnershipIntegrityMismatches`) detects
projection/ownership divergence operationally.
