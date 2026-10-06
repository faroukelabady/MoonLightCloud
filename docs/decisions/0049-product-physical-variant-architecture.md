# 0049: Product & Physical Variant Architecture (Phase 17)

- Status: Accepted (Phase 17)
- Companion: MoonLightRetail ADR-038 (canonical domain model)
- Supersedes: Cloud-side assumptions in 0001-era catalog projection
  documentation that "Product carries SKU/inventory" (projection queries
  documented in Phase 3A/5C/9 ADRs remain historically correct for their
  phases; product-level SKU/stock projection semantics are superseded
  here).

## Context

Retail (the business authority) evolves its catalog model: Product is the
customer-facing aggregate; ProductVariant is the physical sellable unit
(SKU + stock + attribute combination). Cloud projects that model and
provides control-plane/integration services. Cloud must never become
authority for variant state, must preserve strict Store isolation, and
must not multiply stock through provider mappings.

## Decision

1. **Projections.** New tables (migration 00033): `catalog_product_variants`
   (identity, SKU, is_active, tombstone `deleted`, pricing overrides,
   combination_key, variant_revision/catalog_revision, Store-scoped
   UNIQUEs), `catalog_product_variant_attribute_values` (bilingual display
   labels), `catalog_product_variant_inventory` (per-variant stock with its
   own inventory_revision gate), `commerce_product_variant_mappings`
   (stable identity: provider + Store + product + variant + external ids;
   no FK to rebuildable projections). Legacy `store_id NULL` semantics
   (00023) apply unchanged: unscoped rows never collide with proven-Store
   rows.

2. **Events.** New streams (old versions frozen untouched):
   `catalog.product.snapshot.v2` (v1 minus `sku`, plus deprecated
   `primary_variant_sku` mirror), `catalog.product_variant.snapshot.v1`,
   `inventory.product_variant.snapshot.v1`. Projection mirrors the
   existing claim→project→revision-gate→supersede architecture exactly
   (stale revisions are terminal no-ops; equal-revision semantic drift
   blocks deterministically). Variants are tombstoned (`deleted`), never
   hard-deleted; historical order snapshots are never rewritten.

3. **Availability.** `ComputeVariantAvailability(productEligible,
   variantActive, variantStock, allocationLimit)` — the ONLINE quantity a
   provider receives is the variant's own authoritative stock, never a
   product aggregate copied per variant (stock multiplication is a
   Critical/High-class failure). Category suppression and product
   `sell_online` continue to gate the Product; variant status/stock gate
   the exact choice.

4. **Cloud Admin (Phase 16 conventions).** Three new durable
   Store-targeted command types: `catalog.product.variants.update.v1`,
   `catalog.product.variant.update.v1`,
   `catalog.variant.attributes.update.v1` — stable version, immutable
   target, canonical payload hash, expected revision, Store/device
   binding, idempotent receipt. Retail assigns identity/SKU; Cloud never
   fabricates SKUs. Results return via normal catalog sync (Cloud never
   writes catalog projections from command tables).

5. **Health.** Variant-aware codes: PRODUCT_NO_ACTIVE_VARIANTS,
   PRODUCT_NO_SELLABLE_VARIANT, VARIANT_DUPLICATE_COMBINATION,
   VARIANT_MISSING_SKU, VARIANT_MAPPING_MISSING (defects) vs
   VARIANT_INTENTIONALLY_OFFLINE (informational).

6. **Commerce mapping.** Woo/Shopify map Product→product and
   ProductVariant→provider variation; frame configurations (0047) remain
   non-stocked customizations layered on the selected variant and must
   not multiply variant stock; provider limitations fail safely
   (capability conflict) instead of redefining the domain.

## Consequences

- `catalog_products.sku` becomes the deprecated display mirror;
  uniqueness/authority lives in `catalog_product_variants`.
- Reporting keeps working off frozen sale-line projections; an optional
  variant breakdown joins on frozen variant identity columns.
- Command vocabulary grew; Phase 16 commands remain compatible (strict
  decoding, explicit versioning).
