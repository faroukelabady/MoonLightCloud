# ADR-0041: Store-Scoped Commerce Ownership (Phase 9C)

Date: 2026-10-01
Status: accepted (Phase 9C implementation)

## Context

Phase 9B scoped catalog, inventory/policy, sales, returns, and financial
reporting, but deliberately left commerce integration state unscoped:
`commerce_product_mappings`, `online_orders` (+ children/history/fences),
and provider-facing publication enumeration. A same-SKU product in Store
B could reach Store A's remote provider resource through SKU recovery,
and order reconciliation attributed lines without any Store notion.

## Decision

Derive commerce Store ownership exclusively from trusted MoonLight
ownership, never from provider payloads:

- Mapping: `mapping.store_id = catalog_product.store_id` at creation
  (NULL for legacy products). Same-pair identity `(provider_key,
  product_id)` and external uniqueness `(provider_key,
  external_product_id)` are unchanged. A remote external ID can never be
  owned by two MoonLight Stores.
- Publication: `SyncProduct` takes one explicit product whose Store is
  already authoritative (existing CLI shape is already single-product, so
  no CLI redesign). Legacy NULL mappings adopt via the idempotent
  same-pair path; owned mappings never rewrite (cross-Store attempt =
  `STORE_SCOPE_CONFLICT`).
- One provider instance cannot host same-SKU products from two Stores:
  SKU preflight/recovery is metadata-pinned (product ID + provider key)
  and fails closed. Independent provider instances on independent
  remotes stay independent.
- Orders: root `store_id` derives at reconciliation from unanimous
  resolved mapping ownership. Mixed evidence blocks terminally
  (`COMMERCE_STORE_SCOPE_CONFLICT`) with zero writes; unproven orders
  stay NULL; legacy NULL adopts once on deterministic proof; established
  Stores are immutable; tombstones preserve ownership and never block.
- Reads: Store-scoped list/detail/count methods; root ownership controls
  the whole graph (legacy NULL never matches; cross-Store detail reads
  404). Global reads stay ALL+legacy for administration. No HTTP change:
  Store selection waits for 9D.

## Non-goals (deferred)

Store-aware order routing/allocation, provider Store routing, per-Store
credentials, Store notifications/schedules, consolidated accounting,
Shopify, schedulers/workers, dashboard Store UX.

## Consequences

- Same-SKU cross-Store takeover is impossible at adapter, mapping, and
  reconciliation layers (proven live and under race).
- Mixed-Store orders fail safely with no partial state.
- Webhook inbox stays provider-global (Store unknown at receipt is
  truthful); dedupe/hash semantics frozen.
- Availability formula, safe-zero, money arithmetic, and inventory
  authority are untouched; ownership gates use local DB state only.
