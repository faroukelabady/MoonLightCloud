# ADR-0048: Cloud Admin Catalog Control (Phase 16)

Date: 2026-10-05
Status: proposed (Phase 16 implementation; freeze review pending)

## Context

Retail owns catalog truth in SQLite; Cloud owns projections plus
commerce orchestration. Operators need remote control of existing
catalog state (details, prices, classification, online policy,
categories, tags, frame configurations) without opening inbound
connections to Retail and without making Cloud a second catalog
authority. Phase 7C already provides a durable Retail-pulled device
command channel (Sync Now), but its single-active-per-device table
cannot carry a multi-command business queue. No Retail→Cloud
capability advertisement exists (Cloud advertises sync capabilities
to Retail only).

## Decision

Add a parallel-but-consistent command plane rather than overloading
`device_control_commands`:

- Cloud migration 00032: `catalog_admin_commands` (immutable intent:
  id, store_id, versioned type, entity, payload, hash, expected
  revision, actor) + `catalog_admin_command_targets` (immutable
  per-device snapshot with independent outcomes) +
  `catalog_admin_device_capabilities` (Retail-announced support).
- Retail migration 000012: append-only `admin_command_receipts`
  (command_id PK, hash, status, result, pre/post revision).
- Eight versioned types (`catalog.*.v1`); no generic patch; typed
  server-side request models on both ends.
- Retail pulls bounded due targets over existing device auth in the
  existing control worker rhythm; announces
  `catalog_admin_commands_v1`; dispatches through canonical Product,
  Category, Tag and Configuration services with expected-revision
  fencing; persists receipts with crash recovery (RUNNING→RECEIVED
  re-examination, revision fence prevents double-apply).
- Cloud verifies entity Store ownership against projections at
  creation (legacy NULL never mutable); snapshots bound devices as
  targets; gates delivery on active + bound-to-command-Store +
  capable; binds ACKs server-side to the calling device.
- Aggregate states distinguish PENDING/DELIVERED/APPLIED/CONVERGED
  (projection reached post_revision)/PARTIAL/CONFLICT/
  BLOCKED_CAPABILITY/CANCELLED; cancellation only pre-apply.
- Out of scope: remote Product creation (second SKU allocator risk),
  hard deletes, inventory/Sales/Returns mutation, media pipeline,
  self-update (Phase 17), retention policy (Phase 19).

## Consequences

- Cloud stays projection-only for catalog state (zero projection
  writes from admin paths; verified by tests).
- Revision conflicts are terminal at Retail; stale Cloud views can
  never overwrite newer local edits.
- Duplicate delivery, ACK loss and crashes are safe via durable
  receipts plus revision fencing.
- Mixed-version Stores show honest BLOCKED_CAPABILITY instead of
  silent partial success.
- Frame, DAG, money and history invariants reuse Phase 13/15
  validators unchanged.
