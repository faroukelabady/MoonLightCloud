# Shopify Commerce Adapter — Operations Guide (Phase 11)

Shopify is the second concrete `CommerceProvider` instance alongside
WooCommerce. Cloud is the only integration boundary: MoonLightRetail
never knows Shopify exists, stores no Shopify credentials or IDs, and
never calls Shopify.

Core invariant: **MoonLight owns business identity and inventory truth;
Shopify owns only provider state.** Shopify SKU never owns MoonLight
product identity, Shopify inventory never owns Retail stock, Shopify
orders never become Retail Sales, and Shopify payloads never choose the
MoonLight Store.

## Prerequisite: Shopify app with Admin API access

Phase 11 assumes operator-provisioned credentials (no OAuth UI):

1. In the Shopify admin (or Partner dashboard) create a **custom app**
   on the target shop.
2. Grant the minimum required access scopes (see below).
3. Install the app and reveal the **Admin API access token**
   (`shpat_…`).
4. Reveal the app's **API secret key** (client secret) — required only
   when order webhooks are enabled. It is a different credential from
   the access token.

Provision both into the Cloud runtime environment (secret manager /
container secrets). They are runtime-only: never persisted to the
database, logs, metrics, CLI output, or any response.

### Minimum scopes

| Scope | Used for |
|---|---|
| `read_products` | product/variant/metafield reads (ownership verification, SKU recovery) |
| `write_products` | product/variant/metafield writes, publication changes |
| `write_inventory` | `inventoryActivate` + `inventorySetQuantities` at the configured location |
| `write_publications` | `publishablePublish` / `publishableUnpublish` on the configured publication |
| `read_orders` | `GetOrder` (webhook-driven order reconciliation) |

Do not request customer, theme, discount, or other unrelated scopes.

## Configuration

All values are environment configuration (`internal/config/shopify.go`):

| Variable | Example | Notes |
|---|---|---|
| `COMMERCE_SHOPIFY_ENABLED` | `true` | disabled by default |
| `COMMERCE_SHOPIFY_PROVIDER_KEY` | `shopify-main` | logical instance key (1–64 `[a-z0-9_-]`); never the vendor name. Future instances: `shopify-egypt`, `shopify-secondary` |
| `COMMERCE_SHOPIFY_SHOP_DOMAIN` | `example.myshopify.com` | canonical domain only: no scheme, userinfo, port, path, query, fragment, whitespace. The endpoint is constructed internally |
| `COMMERCE_SHOPIFY_API_VERSION` | `2026-10` | only supported `2026-10`; other handles refused |
| `COMMERCE_SHOPIFY_ACCESS_TOKEN` | | Admin API token (secret) |
| `COMMERCE_SHOPIFY_CLIENT_SECRET` | | webhook HMAC secret (secret; required when orders enabled, must differ from the token) |
| `COMMERCE_SHOPIFY_CURRENCY` | `EGP` | provider price currency: exactly one MoonLight price is selected, never a fallback |
| `COMMERCE_SHOPIFY_LOCATION_ID` | `gid://shopify/Location/…` | the only inventory location ever written |
| `COMMERCE_SHOPIFY_PUBLICATION_ID` | `gid://shopify/Publication/…` | the only publication ever published/unpublished |
| `COMMERCE_SHOPIFY_HTTP_TIMEOUT` | `15s` | bounded 1s–120s |
| `COMMERCE_SHOPIFY_ORDERS_ENABLED` | `true` | enables `GetOrder` + the webhook route |

Woo + Shopify can run together (distinct provider keys). One MoonLight
product may map into both providers simultaneously; mappings are
independent.

## API contract

- **API**: Shopify GraphQL Admin API only (no legacy REST Admin).
- **Version**: pinned per `COMMERCE_SHOPIFY_API_VERSION` (latest stable
  at implementation time: `2026-10`).
- **Endpoint**: `https://<shop>.myshopify.com/admin/api/<version>/graphql.json`.
- **Authentication**: `X-Shopify-Access-Token` header. The token never
  appears in URLs, query strings, logs, or errors.
- **Transport**: HTTPS only, redirects refused (the credential is never
  forwarded to another destination), bounded 1 MiB responses, bounded
  timeout, `context` propagation, static GraphQL documents with
  variables-only dynamic values.
- **Errors**: HTTP 200 does not imply success. Top-level `errors[]`
  (incl. `THROTTLED` → retryable rate-limited with a bounded derived
  wait), mutation `userErrors[]` (validation), missing data, malformed
  JSON, and oversized bodies are separate classified layers.
- **Startup**: zero Shopify network calls. An unreachable Shopify never
  prevents Cloud from starting. The shop-currency check runs lazily
  before the first product price write.

## Product publication

One MoonLight product = one Shopify product with exactly one
MoonLight-managed variant (SKU = MoonLight SKU, price = configured
currency). Title: Arabic name primary, English fallback. Description:
Arabic → English → empty. Unicode is preserved exactly. Cost is never
sent.

Manually managed Shopify state is preserved: tags, collections,
images/media, SEO, vendor, product type, unrelated metafields, manual
variants, and unrelated publications are never written or cleared
(`productSet` list-replacement semantics are used ONLY for create; all
updates use targeted mutations).

### Ownership metadata (namespace `moonlight`)

| Key | Meaning |
|---|---|
| `product_id` | MoonLight Product ID (permanent identity half 1) |
| `provider_key` | logical provider instance (permanent identity half 2) |
| `product_operation_key` | current operation identity (retry metadata) |
| `catalog_revision` / `policy_revision` | current revisions (metadata) |
| `managed_variant_id` | the MoonLight-managed variant identity |

Permanent remote ownership = **MoonLight Product ID + ProviderKey**.
SKU alone never proves ownership. Operation key is never identity.

### Recovery and conflicts

- Unmapped product: exact-SKU lookup (search results are re-filtered by
  exact equality). 0 matches → create; 1 owned candidate → recover;
  foreign/unowned or multiple candidates → **Conflict** (never guess,
  never take over a same-SKU product belonging to another Store).
- Mapped product: ownership metafields verified before any write;
  missing remote product → **Conflict** (no replacement create, no
  remap — operator action required); mutation identity mismatch →
  **Conflict**.
- Ambiguous create (response lost after remote commit) and
  mapping-persistence failure both recover the same remote product on
  retry: remote creations stay at 1.

### Publication semantics

MoonLight manages only `COMMERCE_SHOPIFY_PUBLICATION_ID`:

- active + sell_online → published on the configured publication;
- inactive or sell_online=false → unpublished from the configured
  publication, managed inventory set to 0, mapping retained, product
  never deleted.

**Global status cross-channel note**: publishing requires the product to
be `ACTIVE` in Shopify. On update MoonLight changes global status only
in the one direction needed for publication (`DRAFT`/`ARCHIVED` →
`ACTIVE` when publishing), because Shopify otherwise refuses to publish.
Setting `ACTIVE` can make the product visible on OTHER sales channels
that already had it published; unpublication deliberately does **not**
set `DRAFT` (that would silently unpublish unrelated channels).

## Inventory semantics (safe-zero)

- Authority: MoonLight Retail stock → Phase 5C/9 derived
  `OnlineAvailable`, consumed verbatim. Shopify quantities are a
  downstream projection only: no Shopify event ever mutates Retail or
  Cloud stock.
- Location: only `COMMERCE_SHOPIFY_LOCATION_ID`, only the managed
  variant's inventory item, only the `available` quantity.
- Sequence: safe-zero managed inventory → product metadata → publication
  state → restore current availability. At every failure point after a
  remote product exists, managed inventory is **≤ 0** until recovery:
  failure is always "unavailable", never oversold.
- New products are created at quantity 0; inactive / not-ready /
  zero-allocation states always write 0.
- Inventory writes use observed-quantity CAS and re-read the physical level
  after every response. Cached idempotency success is not current stock
  evidence. Correction keys bind logical intent to the observed level
  version, quantity and target. Four attempts bound convergence.

## Concurrent synchronization and durable uncertainty

Product sync serializes the whole provider/Product sequence with PostgreSQL,
then reads canonical desired state. Each mutation's barrier is committed before
HTTP, outside the advisory transaction. A two-minute callback deadline bounds
connection/lock occupancy. A response loss or process death cannot erase the
committed send evidence.

An active `in_flight` or `uncertain` barrier blocks same-provider/Product sync
before network access. No TTL, startup reset or repeated operation key releases
it. Malformed/incomplete outcomes, timeouts, cancellations, 5xx and response
read failures remain blocked. Definitive acknowledged outcomes release only
the exact request record. Other Products/provider keys are unaffected.

Inspect the active request:

```sh
moonlight-cloud commerce product-sync-status --provider shopify-main --product PRODUCT_UUID
```

Only after independently confirming the exact remote request has completed
(or was never applied), record that settlement:

```sh
moonlight-cloud commerce resolve-product-sync \
  --provider shopify-main --product PRODUCT_UUID --operation OPERATION_UUID \
  --resolution remote_completed --confirm-remote-settled
moonlight-cloud commerce sync-product --provider shopify-main --product PRODUCT_UUID
```

Use `remote_not_applied` only with evidence of that outcome. Current Product
values, waiting, local cancellation, or restart are not proof. If settlement
cannot be established, leave the Product blocked. The confirmation is a trusted
operator assertion; the command does not independently verify Shopify's internal
request lifecycle. There is no force/expiry bypass or automatic resend.

Resolution serializes with live work, records durable history, and names the
exact request UUID. Replaying the same resolution is idempotent; a contradictory
resolution fails. Resolving an old request cannot clear a newer barrier.

### Schema 27 deployment and rollback

Migration 00027 is append-only; 00001–00026 and existing business rows remain
unchanged. Stop old product-sync writers and establish legacy request settlement
before migrating and enabling the new writers. Do not overlap old/new product
mutation protocols; already-running old binaries cannot enforce the barrier.

**What the schema check does and does not protect.** The schema-version
check refuses startup of an old `serve` binary against schema 27. That
protection covers `serve` only: older commerce CLI binaries open PostgreSQL
and wire their services **without** verifying the schema version and without
honoring the mutation barrier. Migration alone therefore does not disable any
older executable. During cutover you must additionally:

- retire or disable old CLI artifacts, scheduled jobs, scripts, and any
  manual mutation writers (for example ad-hoc `commerce sync-product`
  automation);
- quiesce already-running old writers before migrating;
- confirm outstanding legacy remote requests are settled on the Shopify
  side before enabling barrier-aware writers;
- treat mixed old/new mutation writers as unsupported at all times.

Only the new barrier-aware writers may run against schema 27.

Back up `commerce_product_mutation_barriers`. Do not delete it during projection
rebuilds or to retry a request. Rollback refuses active uncertainty or retained
resolution history; only an empty-table development rollback is automatic.
Production downgrade requires an explicit preservation/restore plan.

## Order ingestion

Order lines and fuzzy SKU searches are complete paginated reads: 50 nodes
per page, at most 40 pages / 2,000 nodes, one MiB per response and two
minutes per connection. Invalid progress/incomplete retrieval fails without
partial projection or speculative creation. These fixed safety limits must
be reviewed when exceeded; excess input is never silently truncated.


Direction is strictly **Shopify → MoonLightCloud read/projection**.
There are no Shopify order/fulfillment/refund/payment writes.

- External order identity: canonical decimal Shopify resource id
  (the GID suffix == `legacyResourceId`; documented normalization, see
  ADR-0044). Webhook and `GetOrder` paths derive the identical
  identity.
- Money: shop-money side of every MoneyBag, parsed to exact int64 minor
  units (no floats; >2^53 exact). One currency per order, never mixed
  with presentment money, no FX conversion.
- Product resolution: provider key + external Product id →
  `commerce_product_mappings` (never SKU). Store ownership derives from
  those mappings; mixed-Store orders block with
  `COMMERCE_STORE_SCOPE_CONFLICT`; unresolved lines stay NULL (never
  guessed).
- Cancellation reconciles into the canonical order state; it never
  creates MoonLight Sales/Returns, never touches inventory, and never
  enters financial reporting. Shopify orders remain operational provider
  state only.
- Deleted products on historical lines leave the line unresolved but
  retained.

### Order status mapping

| Shopify (financial / fulfillment) | Canonical |
|---|---|
| (cancelled) | `CANCELLED` |
| `REFUNDED` | `REFUNDED` |
| `VOIDED` / `EXPIRED` | `FAILED` |
| `PAID` + `FULFILLED` | `COMPLETED` |
| fulfillment `ON_HOLD` | `ON_HOLD` |
| `PAID`, `PARTIALLY_REFUNDED` | `PROCESSING` |
| `PENDING`, `AUTHORIZED`, `PARTIALLY_PAID` | `PENDING` |
| anything unknown | `UNKNOWN` (raw preserved) |

## Webhooks

Route: `POST /api/v1/commerce/webhooks/shopify/{provider_key}`
(registered only when Shopify + orders are enabled; 404 otherwise).

Topics (header `X-Shopify-Topic`, never the body):

| Topic | Handling |
|---|---|
| `orders/create` | durable delivery → async `GetOrder` reconciliation |
| `orders/updated` | same |
| `orders/cancelled` | same (update path; cancellation is a state change) |
| `orders/delete` | confirmed-deletion semantics (tombstone only on provider-confirmed absence) |

Security and durability:

- HMAC: `X-Shopify-Hmac-SHA256` = base64(HMAC-SHA256(client secret,
  raw body)), constant-time comparison over the exact raw bytes.
  Verified **before** dedupe; invalid/missing/malformed → 401 with zero
  persistence.
- Shop binding: `X-Shopify-Shop-Domain` must equal the configured shop
  domain (fail-closed 401). A validly signed webhook for another shop
  never enters this provider instance.
- Delivery identity: `X-Shopify-Webhook-Id` (bounded printable). Dedupe
  key = (provider key, delivery id): identical redelivery is idempotent,
  same id with different payload hash → 409.
- Body bound 1 MiB → 413 with zero DB writes.
- The raw body is **never** persisted: the inbox stores provider key,
  delivery id, topic, external order id, payload hash, timestamps. No
  customer PII reaches the inbox, logs, metrics labels, or error text.
- Ordering is not guaranteed by Shopify: the frozen generation/lease
  fencing and semantic revisioning converge out-of-order deliveries;
  a transient `GetOrder` failure is never treated as deletion.

## Manual webhook subscription setup

Cloud never registers subscriptions at startup (zero startup network
dependency). Create them manually per shop, e.g. via the GraphQL Admin
API (`webhookSubscriptionCreate`, requires `write_publications`-class
app permission on webhooks) or the app's subscription config:

```
topic: orders/create    → https://<cloud-host>/api/v1/commerce/webhooks/shopify/<provider_key>
topic: orders/updated   → same
topic: orders/cancelled → same
topic: orders/delete    → same
```

Use format `JSON`. The same endpoint serves all four topics. Verify
deliveries with the app's API secret key configured as
`COMMERCE_SHOPIFY_CLIENT_SECRET`.

## Rate limiting

GraphQL Admin API throttling is cost-based; a throttled request returns
HTTP 200 with `errors[].extensions.code = "THROTTLED"`. Cloud classifies
it as retryable rate-limited with a bounded deterministic wait derived
from `extensions.cost.throttleStatus` (never busy-loops; the shared
order processor and sync retries use exponential backoff). No API call
bursting is performed by the adapter: one product sync is a bounded
number of GraphQL operations.

## Credential rotation

1. Rotate the Admin API token (or client secret) in the Shopify admin.
2. Update the corresponding environment variable on Cloud.
3. Restart Cloud (construction performs no network calls, so startup is
   safe regardless of rotation timing).
4. Tokens/secrets are never persisted anywhere, so no data cleanup is
   required. Webhook deliveries signed with the old secret fail closed
   (401) until the new secret is configured.

## API-version upgrade runbook (quarterly)

Shopify releases a new stable version every quarter and supports each
for ≥12 months. Before changing `COMMERCE_SHOPIFY_API_VERSION`:

1. Run the full Cloud test suite (`./scripts/test.sh`) and the Shopify
   adapter suites against the candidate version.
2. Review the version's release notes for the operations used here
   (`productSet`, `productUpdate`, `productVariantsBulkUpdate`,
   `metafieldsSet`, `publishablePublish`/`Unpublish`, `inventoryActivate`,
   `inventorySetQuantities`, `order`, webhook payloads).
3. Verify the opt-in live E2E against a development shop
   (`MOONLIGHT_SHOPIFY_E2E=1`, see below).
4. Bump the pinned version deliberately; never float to `latest`. A
   retired version silently falls forward on Shopify's side — treat any
   missing or mismatched `X-Shopify-API-Version` as a rejected response.
   This build supports only `2026-10`; another release requires a deliberate
   code/configuration contract update.

Notable past changes: idempotency keys became required on
`inventoryActivate`/`inventorySetQuantities` as of `2026-04`
(`@idempotent` directive) and compare-and-set moved to
`changeFromQuantity` (the adapter targets the `2026-04+` shape).

## Troubleshooting

| Symptom | Meaning / action |
|---|---|
| `shopify shop currency X does not match configured currency Y` | shop currency changed; block is intentional — align `COMMERCE_SHOPIFY_CURRENCY` or the shop |
| `shopify product owned by another product or provider` | remote ownership metadata disagrees with the mapping; investigate before any manual edit (no write was made) |
| `shopify product no longer exists` | mapped remote product deleted; operator must resolve (recreate manually + remap, or retire the mapping) |
| `sku … owned by another product or provider` | same-SKU foreign remote product; expected conflict — never taken over |
| `shopify inventory compare mismatch` | concurrent quantity change between safe-zero and restore; retryable, self-healing |
| webhook 401 | signature, secret, or shop-domain mismatch; verify `COMMERCE_SHOPIFY_CLIENT_SECRET` and the subscription's shop |
| webhook 409 | same `X-Shopify-Webhook-Id` with a different payload hash — inspect the delivery source |
| order blocked `COMMERCE_STORE_SCOPE_CONFLICT` | order spans product mappings from two Stores; resolve the catalog ownership |

## Product options (bundles)

Since Phase 15 a Product with frame configurations is published as a
**bundle** (tracked base component + untracked frame component), so every
choice consumes one base inventory pool. Bundle mutations are asynchronous
and recorded as durable receipts. See `docs/operations/product-options.md`.

Since Phase 17 a ProductVariant maps to a Shopify variant with its own SKU
and stock, and durable identity is recorded in
`commerce_product_variant_mappings`. A provider combination the shop cannot
represent fails safely with a capability conflict. See
`docs/operations/product-variants.md`.

## Backup significance

- `commerce_product_mappings`, `commerce_online_orders` and the webhook
  inbox are durable Cloud state and belong in database backups.
- Shopify access token and client secret are **never** in the database;
  restoring a backup to a new environment requires re-supplying both
  environment variables.
- Shopify-side product state (ownership metafields, managed variant,
  inventory at the configured location) is recoverable projection: after
  a restore, re-running `commerce sync-product --provider <key>` for
  affected products reconverges without duplicating remote products.

## Opt-in live E2E

Live testing runs only with explicit opt-in against a dedicated
development/test shop:

```
MOONLIGHT_SHOPIFY_E2E=1
COMMERCE_SHOPIFY_SHOP_DOMAIN=…  COMMERCE_SHOPIFY_ACCESS_TOKEN=…
COMMERCE_SHOPIFY_CLIENT_SECRET=…  COMMERCE_SHOPIFY_LOCATION_ID=…
COMMERCE_SHOPIFY_PUBLICATION_ID=…
```

Never paste secrets into chat or commits. The deterministic local TLS
GraphQL harness is **not** a live test.
