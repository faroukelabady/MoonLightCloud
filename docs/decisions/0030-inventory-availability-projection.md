# ADR-0030 — Product Inventory Projection & Availability (Phase 5C, Cloud)

Date: 2026-09-26
Status: accepted
Scope: Phase 5C (Cloud side; Retail authority in ADR-027)

## Context

Retail emits `inventory.product.snapshot.v1` events carrying authoritative
per-product stock at an independent monotonic `inventory_revision`.
Cloud must project last-known inventory and compute provider-neutral
ONLINE availability from lifecycle + policy + stock, without becoming a
stock ledger, mirroring movements, reserving, or touching providers.

## Decision

1. **Fifth processor, same mechanism.** `inventory_product_projection.v1`
   reuses `sync_event_processing`, claim/lock/mark, backoff, CLI, lazy
   backfill, and `projection status`. No second framework.
2. **Independent revision stream.** Ordering, stale no-op, equal-revision
   semantic identity/conflict, and supersede mirror ADR-0028/0029 but
   version on `inventory_revision` via the parameterized revision scan.
   Lock namespace `product-inventory:<id>` keeps inventory decisions
   serializable per product without joining product/policy lock traffic.
3. **Product dependency, retryable wait.** Inventory arriving before its
   core product yields `CATALOG_DEPENDENCY_WAIT` and converges — never
   terminal, no partial row. Inactive products and `sell_online=false`
   still project inventory rows: lifecycle/policy affect availability,
   never inventory truth. Stock mirrors the Retail constraint exactly
   (`>= 0`, 0..MaxInt32).
4. **Availability is a derived read.** `online_available` computed from
   current product + policy + inventory in one read transaction; no
   availability revision, no writes, no reservations, no allocation
   consumption:
   missing product/policy/inventory → 0 + not-ready; inactive or
   !sell_online → 0; else `max(stock,0)`, capped by the allocation
   limit when set (NULL = uncapped). Safe-zero defaults everywhere;
   no time-based staleness invented.
5. **Read service for Phase 6.** `GetProductInventory` /
   `GetProductAvailability` expose source metadata (revision,
   projected_at, event id); no HTTP surface, no dashboard, no provider
   names in 5C. Consumers treat the quantity as last-known synchronized
   availability with freshness metadata.
6. **Rebuild** clears the inventory table and resets the inventory
   processor; inbox replays deterministically (highest valid revision
   wins regardless of order).
7. **Canonical processor registry.** The `projection status`/`retry`
   allowlists (which diverged in 5B, omitting the policy processor from
   retry) become one canonical list covering all seven processors.

## Consequences

- Phase 6A consumes the availability service behind the modular-monolith
  boundary; multi-store identity evolves in Phase 8 if needed.
- Historical Sale/Return snapshots and reports never join current
  inventory.
