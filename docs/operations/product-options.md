# Product Options — Frame Configurations (Phase 15)

Operations guide for publishing Retail-authored ONLINE product options
(frame configurations) to WooCommerce and Shopify. See ADR-0047 for the
rationale and contracts.

## What a configuration is

A configuration is a valid frame choice for one canonical MoonLight
Product: a style/colour combination with a per-currency price delta and an
`enabled` flag. It is **not** a Product — it carries no SKU and no
inventory. The single physical papyrus stock stays on the canonical
Product.

The implicit **No Frame** choice is not a MoonLight row; it is represented
on the provider side under the reserved mapping key
`00000000-0000-0000-0000-000000000000`.

## Retail contract

Retail emits `catalog.product.configuration.snapshot.v1` with the complete
configuration state at one monotonic `configuration_revision`:

```json
{
  "product_id": "uuid",
  "configuration_revision": 4,
  "configurations": [
    {
      "configuration_id": "uuid",
      "kind": "frame",
      "style_code": "S1",
      "style_name_ar": "نمط",
      "style_name_en": "Style",
      "color_code": "C1",
      "color_name_ar": "لون",
      "color_name_en": "Colour",
      "price_delta_egp_cents": 5000,
      "price_delta_usd_cents": 200,
      "enabled": true,
      "position": 0,
      "configuration_revision": 4
    }
  ]
}
```

Rules enforced at ingestion and projection: UUID identities (canonical
spelling, sentinel rejected), `kind = frame` only, bounded codes/labels,
exact non-negative integer minor units (no floats, no FX), unique
style+colour per Product, explicit rows only (never a Cartesian
generation).

## Projection and convergence

The configuration event projects through
`catalog_product_configuration_projection.v1`, sharing the catalog
processor machinery (dependency wait when the Product is not projected
yet, revision arbitration, Store-scope authorization). Each accepted
revision replaces the Product's configuration set and enqueues a coalesced
commerce re-evaluation.

Check processor state with the usual catalog tooling:

```sh
moonlight-cloud projection status
```

A product configuration is published by the existing sync path; there is
no separate CLI:

```sh
moonlight-cloud commerce sync-product --provider <key> --product <uuid>
```

Publication remains gated by the Product's effective ONLINE eligibility
(active, `sell_online`, and every relevant category node enabled).

## Pricing

Configured price = base Product price + delta, per currency, in exact
integer minor units. Currencies are never summed and never FX-converted.
A currency with no configured delta uses the base price.

## Provider representation

### WooCommerce

One **variable** Product represents the Product. Variations never carry
their own quantities, so No Frame and every frame choice draw from the
single physical parent stock pool. MoonLight-owned variations are matched
by `_moonlight_configuration_id` metadata; manually created or foreign
variations are left untouched. Disabled or missing-currency choices are
converged out of customer selection without deleting mapping state.

### Shopify

The sellable remote Product is a **bundle**:

- the base Product is the only tracked-inventory component — its managed
  variant carries the canonical availability;
- an untracked frame component Product carries the valid combinations plus
  No Frame;
- the bundle parent is the customer-facing identity (title/description
  converge on every pass; status becomes `ACTIVE` only when publication is
  requested).

Configuration identity is the bundle-parent variant GID, stored in durable
configuration mappings. If the shop cannot represent the bundle surface,
the sync reports the stable capability state
`SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE` instead of approximating
with unsafe native variants — a configuration/shop problem, not a transient
failure.

Bundle mutations are asynchronous. A 2xx only acknowledges the request;
the durable receipt records the operation and is polled until it completes.
A returned Product is retained even on failure, so a failed create fences
replacement creation until an operator reconciles it. See
`docs/operations/shopify.md` and ADR-0044.

## Order selections

Online order lines capture the selected frame immutably at ingest
(configuration id, style/colour labels, delta, provider configuration id).
Current configuration rows are never used to reinterpret a past purchase.
An unknown provider selection is preserved raw and marked unresolved, and
an order with an unresolved selection is never reported as fully mapped.
Line reconciliation keeps each line's first captured selection.

## Backup significance

`catalog_product_configurations`, `commerce_product_configuration_mappings`,
and the order-line selection columns are durable state. Configuration
mappings are provider integration state that survives projection rebuilds
and requires normal database backup; the order-line selection snapshots are
immutable history.

## Troubleshooting

| Symptom | Meaning | Action |
|---|---|---|
| `CATALOG_DEPENDENCY_WAIT` on the configuration processor | Core Product not projected yet | Wait — converges automatically |
| `CATALOG_REVISION_CONFLICT` / "configuration identity owned by another product" | Equal revision with different state, or duplicate `configuration_id` across Products | Fix Retail data; the event stays blocked |
| `SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE` | Shop refuses the bundle surface | Enable bundles for the app/shop or use Woo; block is stable |
| `COMMERCE_MUTATION_UNCERTAIN` | An acknowledged Shopify bundle request is unresolved | Inspect/resolve per `docs/operations/shopify.md` before re-syncing |
| Order shows an unresolved selection | Provider selection not matched to a projected configuration | Informational; the raw selection is preserved |
