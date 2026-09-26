# ADR-0029 — Product Sales-Policy Projection (Phase 5B, Cloud)

Date: 2026-09-26
Status: accepted
Scope: Phase 5B (Cloud side; Retail authority in ADR-026)

## Context

Retail emits `catalog.product.sales_policy.snapshot.v1` events carrying
per-product channel eligibility plus the ONLINE allocation cap at an
independent monotonic `sales_policy_revision`. Cloud must converge to the
highest valid revision per product without coupling to stock,
availability, reservations, prices, or provider identity (Phases 5C/6).

## Decision

1. **Fourth processor, same mechanism.** `catalog_product_sales_policy_projection.v1`
   reuses `sync_event_processing`, claim/lock/mark, backoff, CLI, lazy
   backfill, and `projection status`. No second framework.
2. **Independent revision stream.** Ordering, stale no-op, equal-revision
   semantic conflict, and supersede rules mirror ADR-0028 but version on
   `sales_policy_revision`, never `catalog_revision`. The shared
   entity-revision scan takes the revision key as a parameter.
3. **Product dependency, retryable wait.** A policy whose core product has
   not projected yet yields `CATALOG_DEPENDENCY_WAIT`, converging when the
   product arrives — never terminal. Inactive products still project
   policy rows; lifecycle stays separate.
4. **Terminal validation.** UUID identity, positive revision, nullable
   non-negative whole-unit cap, and the canonical rule (allocation
   requires `sell_online`) reject at ingestion and re-check defensively
   at projection. Equal-revision semantic conflicts block
   deterministically with `CATALOG_REVISION_CONFLICT`.
5. **Current-state replace.** One row per product carrying the highest
   valid revision; atomic upsert or nothing. Rebuild clears the policy
   table and resets the policy processor; inbox replays deterministically
   regardless of order.
6. **Read service only.** `GetProductSalesPolicy` plus `IsStoreEligible` /
   `IsOnlineEligible` helpers (eligibility always conjoins product
   `is_active`). No dashboard, no reporting, no HTTP surface in 5B.
7. **No availability math.** Effective ONLINE availability
   (`min(stock, cap)`) belongs to Phase 5C; this projection never claims
   it.

## Consequences

- Phase 5C combines projected policy caps with synchronized stock.
- Phase 6 maps ONLINE-configured products to providers; channel names
  stay canonical (`STORE`/`ONLINE`), never provider names.
