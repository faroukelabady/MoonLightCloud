# Product & Physical Variants (Phase 17)

Operations guide for the Phase 17 catalog model: a Product is the
customer-facing aggregate, and a **ProductVariant** is the physical
sellable unit (SKU + stock + attribute combination). See ADR-0049 (Cloud)
and MoonLightRetail ADR-038 (business authority).

## Ownership boundaries

- **Retail is authority** for variant identity, SKU, stock and attribute
  combinations. Cloud projects that model and never becomes authority.
- Cloud never fabricates a SKU. `catalog_products.sku` becomes the
  deprecated display mirror; uniqueness and authority live in
  `catalog_product_variants`.
- Store isolation is unchanged: variant rows carry the trusted ingress
  Store (legacy `store_id NULL` never collides with a proven Store).
- Variants are **tombstoned** (`deleted = true`), never hard-deleted;
  historical order snapshots are never rewritten.

## Retail contract

| Event | Meaning |
|---|---|
| `catalog.product.snapshot.v2` | Product aggregate carrying **NO SKU-bearing field at all** (Phase 17-R0: Product has no SKU authority anywhere — ADR-0049); for one release Cloud tolerates-and-ignores a leftover `primary_variant_sku` mirror; v1 stays frozen (its historical `sku` wire field still validates, then is discarded — `catalog_products.sku` was retired by 00035). Phase 17-R2: also carries `product_type_id` (structural type identity, never inferred; empty tolerated for legacy rows only) |
| `catalog.product_variant.snapshot.v1` | Complete ProductVariant set at a variant revision (identity, SKU, active, prices, combination, attributes) |
| `inventory.product_variant.snapshot.v1` | Per-variant stock at an independent inventory revision |
| `sale.finalized.v3` | v2 sale plus per-line frozen variant snapshots (`variant_sku`, sale-time attribute labels, nullable sale-time price override) — sourced ONLY from the event (00036). Phase 17-R2: lines also carry the frozen sale-time ProductType snapshot (00038) |
| `catalog.product_type.snapshot.v1` | Phase 17-R2 structural types (identity, code, translations, status, position, type_revision, allowed dimensions, capabilities; 00037). Retail authority; Cloud projects (ADR-0050) |

## Product SKU retirement (Phase 17-R0)

Product carries **no SKU authority anywhere** (ADR-0049): the projection
column and its Store-scoped uniqueness were dropped (migration 00035),
product read models carry `variant_count`/`derived_stock` instead of
sku/stock, and the product-level `CATALOG_MISSING_SKU` health code is
retired (`VARIANT_MISSING_SKU` is the single SKU health code). Variant
rows carry the SKU. Product stock is **derived only** (SUM of active,
non-tombstoned variant stock at read time). Historical sale-line snapshot
SKUs are immutable history and stay untouched.

Provider adapters still need a product-level SKU (providers require one):
it is **derived** from variant SKU ownership (the first live variant's
SKU). A product with no variant rows carries no SKU and cannot be
published — a documented provider limitation, surfaced by
`PRODUCT_NO_ACTIVE_VARIANTS` health.

## Projection

Two processors share the frozen catalog machinery:

```text
catalog_product_variant_projection.v1
inventory_product_variant_projection.v1
```

Dependency waits are retryable (`CATALOG_DEPENDENCY_WAIT`: the Product or
the variant has not projected yet); stale revisions are terminal no-ops;
equal-revision semantic drift blocks with `CATALOG_REVISION_CONFLICT`; a
variant identity already owned by another Product blocks. Accepted
revisions enqueue the usual coalesced commerce re-evaluation.

```sh
moonlight-cloud projection status
moonlight-cloud projection retry <event-id> catalog_product_variant_projection.v1
```

## Availability

`ComputeVariantAvailability(productEligible, variantActive, variantStock,
allocationLimit)` is the canonical calculation. The ONLINE quantity a
provider receives is the **variant's own** authoritative stock — never a
product aggregate copied per variant (that would multiply stock and is a
critical failure). Category ONLINE suppression and product `sell_online`
still gate the whole Product; variant `is_active`/stock gate the exact
choice.

## Provider representation

- **WooCommerce:** Product → Woo product, ProductVariant → Woo variation,
  with per-variation stock/SKU. Frame configurations (Phase 15) layer on
  the selected variant as non-stocked customization and must not multiply
  variant stock.
- **Shopify:** Product → Shopify product, ProductVariant → Shopify
  variant. Where the provider cannot represent the required combination,
  the sync fails safely with a stable capability conflict instead of
  approximating and redefining the domain.
- Durable variant identity lives in `commerce_product_variant_mappings`
  (provider + Store + product + variant + external ids) — never matched by
  SKU or label. It survives projection rebuilds and requires normal
  database backup.

## Order selections

Online order lines capture the resolved variant immutably at ingestion
(`variant_id`, `variant_sku`, `variant_attribute_snapshot`). Current
catalog rows are never authority for a past purchase, so a later rename,
price change, disable or tombstone cannot rewrite history. Lines with no
provider variation (or an unresolved one) keep truthful NULLs.

## Sale history (sale.finalized.v3)

`sale.finalized.v3` freezes the same identity ON THE SALE LINE at sale
time (`variant_sku`, `variant_attributes` labels, nullable sale-time
price override — migration 00036). The snapshot is sourced ONLY from the
event: no read path joins current catalog state, so renaming a catalog
attribute label never moves history (covered by
`TestSaleV3LabelRenameImmutability`). v1/v2 sales keep truthful NULLs.
`ReportSalesByVariant` groups by the frozen identity/labels.

## Provider limitations (retained; verified with ZERO provider mutations)

Several physical variants **and** frame options together are
**unsupported** on both Shopify and WooCommerce: frame choices cannot
layer on per-variant stock without a (variant × frame) Cartesian set the
durable mappings cannot identify safely. Both adapters refuse BEFORE any
remote write with their stable capability codes
(`SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE` /
`WOO_VARIANT_OPTIONS_CAPABILITY_UNAVAILABLE`) — no provider mutation of
any kind happens (asserted by `TestShopifyVariantFrameCapabilityConflict`
and `TestWooVariantFrameCapabilityConflict`). The full publish→order
chain (`TestShopifyVariantFullChainOrderNeverMutatesStock`,
`TestWooVariantFullChainOrderNeverMutatesStock`) additionally proves
per-variant stock 4/2 is never multiplied and provider orders NEVER
mutate provider/Retail inventory. Supporting the combination
requires provider-side variation-set work plus a durable
variant×configuration mapping; that is future ADR material.

## Shopify managed-variant transitions (Phase 17-R0)

The single-variant shape stamps `moonlight.managed_variant_id` on the
product; the multi-variant shape has none. Reconciliation keeps the
metafield truthful across 1↔N↔1 transitions: multi-variant convergence
durably tombstones it (empty metafieldsSet value — restart-safe, every
reconcile re-runs), single-variant convergence re-stamps it. Variant
identity adoption ALWAYS prefers the variant-scoped
`moonlight.variant_id` stamps; a stale `managed_variant_id` is ignored
and cleaned, never adopted from. Product-level inventory refuses the
multi-variant shape (its stock is variant-scoped).

## Admin control

Operators change variant state through the Phase 16 command plane (see
`docs/operations/catalog-admin.md`), never by writing projections:

```text
catalog.product.variants.update.v1    full variant set for one Product
catalog.product.variant.update.v1     one variant (SKU, active, prices, attributes)
catalog.variant.attributes.update.v1  variant attribute definitions/labels
```

Retail assigns identity/SKU and revision-fences every apply; results return
through normal catalog sync.

## Catalog Health

Defects: `PRODUCT_NO_ACTIVE_VARIANTS`, `PRODUCT_NO_SELLABLE_VARIANT`,
`VARIANT_DUPLICATE_COMBINATION`, `VARIANT_MISSING_SKU`,
`VARIANT_MAPPING_MISSING`. Informational only:
`VARIANT_INTENTIONALLY_OFFLINE`.

## Backup significance

`catalog_product_variants` and its child rows are rebuildable projections
(replay the inbox). `commerce_product_variant_mappings` is durable
integration state that survives rebuilds and belongs in backups, alongside
the order-line variant snapshots (immutable history).

## Troubleshooting

| Symptom | Meaning | Action |
|---|---|---|
| `CATALOG_DEPENDENCY_WAIT` on a variant processor | Product (or variant, for inventory) not projected yet | Wait — converges automatically |
| `CATALOG_REVISION_CONFLICT` on a variant | Equal revision with different state, or identity owned by another Product | Fix Retail data; the event stays blocked |
| `VARIANT_MAPPING_MISSING` in health | Online-eligible variant without a provider mapping | Run `commerce sync-product` (or investigate the provider capability) |
| Provider capability conflict | Provider cannot represent the variant/combination | Adjust the provider product model; the sync blocks rather than degrading |
| Order line shows no variant | Provider order line had no resolvable variation | Informational; the raw line is preserved |
