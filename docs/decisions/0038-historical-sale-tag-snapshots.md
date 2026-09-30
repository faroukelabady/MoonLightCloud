# ADR-0038: Historical Sale Tag Snapshots (sale.finalized.v2)

## Status

Accepted (Phase 8C, Cloud side).

## Context

`sale.finalized.v1` carries category snapshots but no tags, so Cloud
could not answer sale-time tag membership after Retail tag edits.
Retail now emits `sale.finalized.v2` with per-line frozen tag
snapshots; v1 remains frozen and accepted.

## Decision

- Register `sale.finalized.v2` with strict Decode+Validate (all v1
  invariants inherited by conversion, plus tag rules). v1 validation
  is untouched.
- Project under a separate `sale_projection.v2` processor with the
  same durable `sale_event_ownership` arbitration: the first event
  wins regardless of version, losers block with `SALE_ID_CONFLICT`
  and write nothing. Replay uses `ON CONFLICT DO NOTHING`
  throughout, including tag rows and the capture mark.
- Persist `sale_item_tag_snapshots` (no FK to `catalog_tags`) plus
  `sales_projection.tag_capture` (NULL = v1 unknown, TRUE =
  captured). Migration 00021 is append-only; v20 data upgrades with
  NULL capture and zero fabricated rows.
- Expose a `tag` breakdown dimension grouped by historical identity
  (id + slug + both names; renames stay separate rows), reusing the
  category merge pattern. Tag groups overlap by design and are
  documented non-additive. Dashboard overview and scheduled business
  reports are untouched (canonical summary allowlist unchanged).
- Terminal catalog states (renamed/hidden/deleted tags) never affect
  projected history; report queries join snapshot tables only.

## Consequences

- Rollout requires Cloud v1+v2 support before Retail emits v2;
  older Clouds reject v2 deterministically via capabilities.
- Historical tag fidelity holds across catalog churn without any
  tombstone protocol.
