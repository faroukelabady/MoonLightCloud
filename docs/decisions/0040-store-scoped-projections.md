# ADR-0040: Store-Scoped Cloud Projections (Phase 9B)

Date: 2026-10-01
Status: accepted (Phase 9B implementation)

## Context

Phase 9A established trusted Store identity: Retail owns its Store UUID,
Cloud authorizes device membership, and `sync_events.store_id` is derived
server-side from the authenticated device binding (NULL for legacy/unbound
events). Projections, however, remained single-namespace: `UNIQUE(sku)` and
`UNIQUE(slug)` were global, sale IDs arbitrated globally with no ownership
record, and no report could answer per-Store.

## Decision

Propagate the frozen ingress Store context into projection roots:

- Ownership is copied from `sync_events.store_id` only. Never from current
  device bindings, request bodies, payloads, dashboard selection, catalog
  lookups, or provider configuration.
- `NULL` = unscoped legacy (ownership not proven). Historical rows are
  never backfilled from membership.
- Current-state roots (`catalog_categories`, `catalog_tags`,
  `catalog_products`, `catalog_product_sales_policies`,
  `catalog_product_inventory`) carry nullable `store_id`.
- Historical roots (`sales_projection`, `return_refund_projection`) carry
  nullable `store_id`, immutable once written. Children stay FK-owned by
  the root; no duplicated Store columns.
- Relations (edges, subcategories, product tags, prices, translations)
  carry no Store: the projector verifies scope compatibility in-transaction
  (NULL is a legacy wildcard; two proven Stores must match).

## Classification

- Current-state: categories, tags, products, sales policy, inventory.
  May adopt Store ownership once (see below); revisions still arbitrate.
- Historical immutable: sales, sale items/payments/classifications/tag
  snapshots, returns/corrections, ownership rows. Never adopt, never
  rewrite.
- Durable integration state (provider mappings, commerce orders,
  notifications, report schedules, device control, operations): NOT
  Store-scoped in 9B. Online orders are documented storeless; 9C owns
  Store-aware fulfillment.

## Legacy current-state adoption

A `NULL` current-state row adopts ingress Store S when a scoped event for
the same aggregate ID passes the existing revision arbitration (same
continuity evidence the projector already trusts: aggregate identity +
monotonic revision; same-device authorship is a bonus, not a requirement,
since devices rotate). Rationale: UUID aggregate IDs make cross-Store
guessing infeasible, and any genuine post-adoption collision surfaces as
`STORE_SCOPE_CONFLICT` (reject, never overwrite) — the safe direction.
Legacy events keep projecting through COALESCE preservation and never
wipe an adopted Store.

Returns are stricter: a scoped return for a `NULL` legacy sale additionally
requires same-device authorship, else `LEGACY_SCOPE_AMBIGUOUS`. Money
never moves on ambiguity.

## Identity decisions

- `product_id`, `category_id`, `tag_id`, `sale_id`, `return_refund_id`
  remain globally unique arbitration keys (physical PKs unchanged).
  Cross-Store same-ID writes block (`STORE_SCOPE_CONFLICT`,
  `SALE_ID_CONFLICT`, `RETURN_REFUND_ID_CONFLICT`) instead of overwriting.
  Same physical products across Stores share identity; distinct products
  have distinct UUIDs.
- SKU uniqueness is Store-local: `UNIQUE(store_id, sku)`. Same for tag
  slugs: `UNIQUE(store_id, slug)`. PostgreSQL NULL semantics keep legacy
  rows collision-free; same-Store duplicates stay prohibited.

## Conflicts

- `STORE_SCOPE_CONFLICT`: permanent block, no hot-loop (excluded from
  supersede/re-arm predicates), bounded log fields only.
- `LEGACY_SCOPE_AMBIGUOUS`: permanent block for unattributable scoped
  returns.
- Existing revision/conflict taxonomy untouched; Store context is an
  additional invariant, never a replacement for revision ordering.

## Reporting

- New internal `*ForStore` repository methods (same frozen aggregates +
  Store predicate). Tag/category grouping stays snapshot-identity based
  within scope; slugs/names never merge across Stores.
- Unfiltered methods keep documented global behavior (ALL Stores +
  legacy): existing single-Store deployments see identical totals.
- No service, HTTP, dashboard, or OpenAPI change: the Store filter stays
  internal until 9D.

## Migration

- `00023_store_scoped_projections.sql`: nullable columns, RESTRICT FKs,
  partial/composite indexes, uniqueness migration. Zero backfill; all
  existing rows keep values. Down migration honestly reverses schema
  (downgrade with cross-Store SKU/slug collisions will fail on the
  restored global uniqueness constraint).

## Consequences

- Cross-Store same SKUs/slugs coexist with independent inventory,
  policy, and reporting lineage (proven by tests).
- A Store B return can never mutate a Store A sale.
- Rebuilds replay inbox Store context; A/B/NULL ownership preserved.
- Availability stays per-product-ID (store-safe by construction).
