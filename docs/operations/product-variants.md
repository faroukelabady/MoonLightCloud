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
| `catalog.product.snapshot.v2` | Product aggregate **without** `sku` (a deprecated `primary_variant_sku` mirror may appear); v1 stays frozen |
| `catalog.product_variant.snapshot.v1` | Complete ProductVariant set at a variant revision (identity, SKU, active, prices, combination, attributes) |
| `inventory.product_variant.snapshot.v1` | Per-variant stock at an independent inventory revision |

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
