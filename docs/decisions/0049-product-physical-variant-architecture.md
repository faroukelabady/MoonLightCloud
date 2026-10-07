# 0049: Product & Physical Variant Architecture (Phase 17, revised by 17-R0)

- Status: Accepted (Phase 17; amended in place by Phase 17-R0 before
  release — `catalog.product.snapshot.v2` is a frozen Phase 17 *candidate*
  contract, not yet shipped, so its payload was revised in place instead
  of version-bumped)
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

**The one rule (17-R0, unequivocal): Product has NO SKU authority
anywhere — including Cloud projections, Cloud APIs, and provider-neutral
read models. ProductVariant owns SKU and stock. Product stock is DERIVED
only (SUM of active, non-tombstoned variant stock at read time; never
stored, never authoritative). Frames are non-stocked customizations.
Provider limitations are documented, never papered over.**

1. **Projections.** Tables (migration 00033): `catalog_product_variants`
   (identity, SKU, is_active, tombstone `deleted`, pricing overrides,
   combination_key, variant_revision/catalog_revision, Store-scoped
   UNIQUEs — including the Store-scoped SKU uniqueness that used to live
   on `catalog_products`), `catalog_product_variant_attribute_values`
   (bilingual display labels), `catalog_product_variant_inventory`
   (per-variant stock with its own inventory_revision gate),
   `commerce_product_variant_mappings` (stable identity: provider + Store
   + product + variant + external ids; no FK to rebuildable projections).
   Legacy `store_id NULL` semantics (00023) apply unchanged: unscoped rows
   never collide with proven-Store rows. **17-R0 (migration 00035):
   `catalog_products.sku` is RETIRED — the column and its UNIQUE
   (store_id, sku) are dropped.** The historical sale-line snapshot SKU
   (`sale_lines_projection.sku` and the order/return equivalents) is
   immutable history and is deliberately untouched.

2. **Events.** Streams (old versions frozen untouched):
   `catalog.product.snapshot.v2` (v1 minus `sku`),
   `catalog.product_variant.snapshot.v1`,
   `inventory.product_variant.snapshot.v1`.
   **17-R0 payload revision (in place — v2 was a frozen candidate, not yet
   released): the v2 payload carries NO SKU-bearing field at all.** The
   former `primary_variant_sku` display mirror is gone from the contract
   (OpenAPI: strict, `additionalProperties: false`). Compatibility: for
   ONE release Cloud's decoder TOLERATES AND IGNORES a present
   `primary_variant_sku` (Retail may still emit it until both trees ship
   together); the value is never validated, stored, or projected. The next
   release may reject it like `sku`. Frozen v1 keeps validating its
   historical `sku` wire field; the value is then discarded at projection
   (there is no column left to store it). Projection mirrors the existing
   claim→project→revision-gate→supersede architecture exactly (stale
   revisions are terminal no-ops; equal-revision semantic drift blocks
   deterministically). Variants are tombstoned (`deleted`), never
   hard-deleted; historical order snapshots are never rewritten.

3. **Historical sale variant snapshots (`sale.finalized.v3`, 17-R0).**
   Frozen `sale.finalized.v1`/`.v2` are never redefined and stay fully
   supported. `sale.finalized.v3` = every v2 field PLUS, per item, the
   immutable ProductVariant snapshot frozen AT SALE TIME:
   `variant_id`, `variant_sku`, `variant_attributes` (≤ 32 entries of
   definition_code/value_code + bilingual labels), and the nullable
   sale-time `variant_price_egp_cents`/`variant_price_usd_cents` pricing
   override. The snapshot is all-or-nothing per line. History law
   (migration 00036): these columns on `sale_lines_projection` are
   sourced ONLY from the event snapshot — no projection or read path may
   join `catalog_product_variant_attribute_values` (or any current-state
   catalog table) to populate or refresh them. A later catalog
   attribute-label rename can never rewrite what was sold (proved by
   test). Lines without variant data (and all v1/v2 lines) stay truthful
   NULL. Reporting groups by the frozen variant identity/labels
   (ReportSalesByVariant).

4. **Availability.** `ComputeVariantAvailability(productEligible,
   variantActive, variantStock, allocationLimit)` — the ONLINE quantity a
   provider receives is the variant's own authoritative stock, never a
   product aggregate copied per variant (stock multiplication is a
   Critical/High-class failure). Category suppression and product
   `sell_online` continue to gate the Product; variant status/stock gate
   the exact choice. Provider-facing *product* SKU (providers require one)
   is DERIVED from variant SKU ownership — the first live variant's SKU;
   a product with no variant rows carries no SKU and cannot be published
   (documented provider limitation).

5. **Cloud Admin (Phase 16 conventions).** Three durable Store-targeted
   command types: `catalog.product.variants.update.v1`,
   `catalog.product.variant.update.v1`,
   `catalog.variant.attributes.update.v1` — stable version, immutable
   target, canonical payload hash, expected revision, Store/device
   binding, idempotent receipt. Retail assigns identity/SKU; Cloud never
   fabricates SKUs. Results return via normal catalog sync (Cloud never
   writes catalog projections from command tables). Product read models
   (dashboard + catalog admin + OpenAPI) carry `variant_count` and
   `derived_stock` — never product sku/stock; variant rows carry the SKU.

6. **Health.** Variant-aware codes: PRODUCT_NO_ACTIVE_VARIANTS,
   PRODUCT_NO_SELLABLE_VARIANT, VARIANT_DUPLICATE_COMBINATION,
   VARIANT_MISSING_SKU, VARIANT_MAPPING_MISSING (defects) vs
   VARIANT_INTENTIONALLY_OFFLINE (informational). **17-R0: the
   product-level CATALOG_MISSING_SKU code is retired with the column;
   VARIANT_MISSING_SKU is the single SKU health code.**

7. **Commerce mapping.** Woo/Shopify map Product→product and
   ProductVariant→provider variation; frame configurations (0047) remain
   non-stocked customizations layered on the selected variant and must
   not multiply variant stock. **Provider limitation (retained, verified
   by test with ZERO provider mutations on refusal): several physical
   variants AND frame options together are UNSUPPORTED** — the current
   bundle component mapping cannot layer frame choices on per-variant
   stock without a (variant × frame) Cartesian set that the durable
   mappings cannot identify safely. Shopify and Woo fail with their
   stable capability codes (SHOPIFY/WOO `_VARIANT_OPTIONS_CAPABILITY_
   UNAVAILABLE`) before any remote write. Supporting the combination
   requires provider-side variation-set work and a durable
   variant×configuration mapping — future ADR.

8. **Shopify 1↔N variant transitions (17-R0, R08).** The single-variant
   shape stamps a product-level `moonlight.managed_variant_id` metafield;
   the multi-variant shape has no managed variant. During reconciliation
   the shape change durably CLEANS the stale identity: multi-variant
   convergence tombstones the metafield with an empty value through
   metafieldsSet (restart-safe — every reconcile re-runs it), and
   single-variant convergence (re)stamps it. Variant-scoped
   `moonlight.variant_id` stamps are the ONLY adoption proof in variant
   paths: mapping-loss recovery never adopts via `managed_variant_id`
   when N > 1 (a stale value pointing at another variant is ignored and
   tombstoned), and product-level SetInventory refuses the multi-variant
   shape before touching anything. Transitions 1→N, N→1, 1→N→1, restart
   between transitions, and mapping loss during transition never
   duplicate provider resources (tested).

## Consequences

- Product carries no SKU anywhere: projection column dropped (00035),
  DTOs/OpenAPI carry `variant_count`/`derived_stock`, uniqueness lives in
  `catalog_product_variants`. Same-Store product SKU reuse no longer
  blocks at the product level (there is no product SKU to collide).
- Reporting keeps working off frozen sale-line projections; the variant
  breakdown groups by the frozen per-line variant snapshot (labels and
  all) and is immune to catalog renames.
- The provider-facing product SKU is derived, documented, and absent for
  variant-less products (health flags those; providers reject them).
- Command vocabulary grew; Phase 16 commands remain compatible (strict
  decoding, explicit versioning).
- `sale.finalized.v3` is additive; v1/v2 clients keep working. Retail
  emits v3 when it can freeze variant identity at sale time.


## Phase 17 R1 provider representation correction

Woo variable parents use a stable integration-only `MLP-<sha256(provider + NUL + ProductID)>` SKU. Physical SKUs belong to child variations. An existing single physical parent is converted before creating children, and the parent-to-child mapping change uses Store-verified compare-and-set against its prior parent identity. Multiple-to-single retains the owned child representation; recovery verifies provider/Product/Variant metadata. Adding frames to that retained child representation is refused before writes, because the supported frame pool cannot safely change that historical representation. Fresh single-Variant frame pools remain supported.

Provider prices use the physical Variant override when present, including explicit zero, then Product base inheritance; frame deltas are applied separately with checked integer arithmetic. Multiple-Variant parents do not require an unused base price. ProductType report buckets include frozen ID/code/AR/EN labels and canonical Return attribution, including refund-only negative net periods.
