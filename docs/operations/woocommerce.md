# WooCommerce Adapter Operations (Phase 6B)

First concrete `CommerceProvider`: WooCommerce REST API v3 over HTTPS
with HTTP Basic Auth. MoonLight stays authoritative; Woo receives
desired product state and safe availability. Simple products only.

## Configuration

| Variable | Required | Rule |
|---|---|---|
| `COMMERCE_WOO_ENABLED` | no | Default `false`. Disabled registers nothing and needs nothing else |
| `COMMERCE_WOO_PROVIDER_KEY` | when enabled | Generic instance key, e.g. `website`. Never required to be `woocommerce` |
| `COMMERCE_WOO_BASE_URL` | when enabled | `https://` origin, optional subpath. No userinfo, query, or fragment |
| `COMMERCE_WOO_CONSUMER_KEY` | when enabled | Woo REST API key (Basic Auth username) |
| `COMMERCE_WOO_CONSUMER_SECRET` | when enabled | Woo REST API secret (Basic Auth password) |
| `COMMERCE_WOO_CURRENCY` | when enabled | `EGP` or `USD`: which MoonLight price becomes `regular_price`. No silent fallback |
| `COMMERCE_WOO_DIMENSION_UNIT` | when enabled | Only `cm` in 6B |
| `COMMERCE_WOO_HTTP_TIMEOUT` | no | Default `15s`, within `[1s, 120s]` |

Invalid enabled configuration fails startup/config loading, never the
first product sync. Disabled configuration requires no credentials and
constructs no network client.

## Credentials

Woo REST API keys need **Read/Write** permission for product reads and
writes (WordPress admin → WooCommerce → Settings → Advanced → REST API).
Keys belong only in the process environment (Cloud runtime secrets).
They are never persisted to PostgreSQL, never appear in sync events,
logs, CLI output, or URLs, and Basic Auth is sent over HTTPS only.

## Provider key

The adapter's `Key()` returns the configured generic key (e.g.
`website`). Logical instance and implementation type stay separate, so
a second Woo instance later is just another key.

## Currency and dimensions

One Woo store currency per adapter instance: the configured currency
selects the exact MoonLight price (integer minor units → exact decimal
string, no floats). A missing configured-currency price is a validation
failure, never a silent cross-currency fallback. Dimensions map
`width_cm → dimensions.width`, `height_cm → dimensions.height` as
integer strings; `length` stays unset (MoonLight owns no depth);
absent dimensions are omitted, never zero-invented.

## Arabic-primary behavior

Woo `name`/`description` carry the Arabic value with English fallback;
both absent is a validation error. Empty description is allowed.
Phase 6B does not integrate WPML/Polylang: Woo holds the
Arabic-primary storefront value while English stays preserved inside
MoonLight.

## Safe-zero strategy

Every product create/update writes
`manage_stock=true, stock_quantity=0, stock_status=outofstock,
backorders=no` FIRST; the separate `SetInventory` then restores derived
availability. If mapping persistence or inventory update fails, Woo
holds 0/out-of-stock instead of stale sellable stock: temporary
unavailability is favored over overselling. Mapped products are
re-zeroed on every metadata update for the same reason.

## SKU ownership recovery

Before creating, the adapter looks up the exact SKU. Empty → POST;
exactly one product with matching `_moonlight_product_id` +
`_moonlight_provider_key` metadata → recover by update (covers mapping
loss and ambiguous POST failures); foreign or multiple → conflict, no
POST. A duplicate-SKU POST error triggers one bounded recovery lookup
with the same rule. Permanent ownership is ProductID + ProviderKey; the
operation key only identifies desired state.

## Mapping meaning

`commerce_product_mappings` rows are durable integration state shared
with the generic 6A model: same-pair creation is idempotent, remaps
conflict loudly, and rebuilds never delete them. Disabled products keep
their mapping (needed to unpublish the remote product).

## Manual sync CLI

```text
moonlight-cloud commerce sync-product --provider <provider-key> --product <product-uuid>
```

One explicit product, one synchronous run, bounded safe output
(provider, product, outcome, external id, mapping/inventory flags,
operation keys, error classification). No scheduler, no bulk command.
Exit is non-zero on provider errors, conflicts, not-ready products, and
invalid configuration. Credentials are never printed.

## Error classifications

Temporary and rate-limited (with `Retry-After` when Woo sends it) are
retryable by future orchestration; authentication, validation, and
conflict are terminal for the same desired state. Mapped-product 404s
and identity mismatches are conflicts, never silent remaps.

## Error sanitization

Woo `code`/`message` fields are remote-controlled input and are never
copied blindly into operator-visible errors. A centralized scrubber
redacts the configured consumer key/secret (raw, URL-escaped, and
percent-decoded forms) plus the Basic Authorization value before any
`ProviderError` message is built, so the same failure stays safe
whether it surfaces in CLI output, logs, wrapped errors, or future
callers. Classification and `Retry-After` are preserved; context
cancellation is never wrapped. Configuration errors name the rule
(`COMMERCE_WOO_BASE_URL must use https`) without echoing the supplied
value.

## Inventory identity rule

A 2xx inventory response must carry the requested mapped Woo ID
(canonical decimal). Missing, malformed, zero, negative, or overflow
IDs are Temporary failures — the remote may have applied stock, but
the response proves nothing, so `SyncProduct` never reports
`InventoryUpdated=true`. A different valid ID is a Conflict. Retrying
the same inventory update is idempotent; no rollback is attempted.

## Deferred adapter capabilities

Deliberate v1 boundaries, not omissions: Woo category/tag
materialization (MoonLight's multi-parent DAG must not be flattened
lossily), product images/media uploads, multilingual plugins, variable
products, sale pricing, and automatic background synchronization.
Manually managed Woo taxonomy and images are preserved because update
payloads omit those fields entirely (never empty arrays).
