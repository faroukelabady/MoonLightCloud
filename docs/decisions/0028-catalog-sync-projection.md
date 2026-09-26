# ADR-0028 — Product Catalog Sync Projection (Phase 5A, Cloud)

Date: 2026-09-26
Status: accepted
Scope: Phase 5A (Cloud side; Retail authority in ADR-025)

## Context

Retail emits `catalog.*.snapshot.v1` events carrying complete entity state
at monotonic per-entity revisions. Cloud must project mutable current
state (products, category DAG, tags, relations) reusing the frozen
processing architecture, without touching historical Sale/Return snapshots,
reporting, inventory, or providers.

## Decision

1. **Three processors, one mechanism.** `catalog_category_projection.v1`,
   `catalog_tag_projection.v1`, `catalog_product_projection.v1` reuse
   `sync_event_processing` (pending/retry/blocked/processed), claim/lock/
   mark, backoff, CLI, and lazy backfill discovery. No second framework.
2. **Revision ordering.** Higher valid revision replaces current state
   transactionally. Stale revision is a terminal no-op (marked processed,
   never infinite backlog). Equal revision + identical payload hash is
   idempotent; equal revision + different hash is a deterministic
   `CATALOG_REVISION_CONFLICT` block. A blocked old revision never freezes
   the entity: newer events evaluate independently against current state.
3. **Dependency waits.** Missing referenced entities (parent category,
   product's categories/tags) yield retryable `CATALOG_DEPENDENCY_WAIT`,
   converging when dependencies arrive — never terminal, no resend.
4. **DAG defense.** Cloud re-validates: no self-edge, acyclic, max 3-node
   depth, complete parent-set replace (no delta accumulation, no
   UNIQUE(child) tree constraint). Violations block terminally.
5. **Product structural integrity.** Top must be a current root, subs
   reachable from top via current edges; active flags are NOT required
   (mirrors Retail retained-assignment semantics). Violations block
   terminally once dependencies exist.
6. **Atomic projection.** Entity row + relations + revision metadata commit
   together; failure leaves the previous revision visible.
7. **ACK unchanged.** ACK = durable `sync_events` commit; projection never
   required for ACK. Unknown/unsupported types keep frozen 422 behavior
   (Retail defers via capabilities — no Cloud change).
8. **Isolation.** Reporting never joins catalog tables (historical
   snapshots stay authoritative). No provider columns, no stock columns.

## Authority assumption

Retail catalog is the single logical revision stream per entity; devices
are transport identities, not independent masters. Cloud never merges
concurrent business edits from multiple Retail databases: same entity,
same revision, different semantic state blocks deterministically
(`CATALOG_REVISION_CONFLICT`) as defensive integrity.

## Consequences

Rebuilds choose the highest valid revision deterministically regardless of
processing order. Catalog read service (`internal/catalog`) serves future
phases; no public HTTP catalog API and no dashboard work in 5A.
