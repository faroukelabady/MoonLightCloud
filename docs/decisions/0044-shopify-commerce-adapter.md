# ADR-0044: Shopify Commerce Adapter & Online Order Integration (Phase 11)

Date: 2026-10-02
Status: accepted (Phase 11 implementation; freeze review pending)

## Context

Phase 6A established the provider-neutral commerce boundary
(`commerce.CommerceProvider` with `Key()`/`UpsertProduct()`/
`SetInventory()`, durable `commerce_product_mappings`, deterministic
operation keys, and the `ProviderError` taxonomy); Phase 6B proved it
with the WooCommerce REST adapter; Phase 6C added the provider-neutral
online-order domain (`orders.CommerceOrderProvider.GetOrder`, normalized
`OrderSnapshot`, semantic fingerprints, durable webhook inbox with
delivery dedupe and lease-fenced reconciliation); Phase 9 added Store
ownership to mappings and order derivation; Phase 5C/9 owns derived
`OnlineAvailable` availability.

Phase 11 adds Shopify as the second concrete commerce provider alongside
WooCommerce with **no second commerce subsystem**, no schema change, and
no MoonLightRetail change. The frozen Phase 10 pair is Retail
`6afe50a079f560c12f2f2080e42d4922efe96f6e` / Cloud
`ee58687449d8da737c7ce2708a6decdede7671cd`; Cloud schema stays 26.

## Decision

### GraphQL-only adapter

Shopify integration uses the **Shopify GraphQL Admin API** at a pinned
`YYYY-MM` version (`2026-10` at implementation time), with `net/http` +
`encoding/json` only (no Shopify SDK). Legacy REST Admin endpoints are
not used. Dynamic values travel as GraphQL variables over statically
defined documents. HTTPS is mandatory; redirects are refused so the
access token never crosses origins; response bodies are bounded (1 MiB);
HTTP 200 never implies success (top-level `errors[]`, mutation
`userErrors[]`, missing data, malformed JSON, and oversized bodies are
separate classified layers; `THROTTLED` maps to the frozen rate-limited
kind with a bounded deterministic hint).

### Reuse of the frozen provider-neutral boundaries

`internal/commerce/shopify` implements exactly the frozen
`commerce.CommerceProvider` (and the optional
`orders.CommerceOrderProvider` capability) — no
`ShopifyProductProvider`, `SetShopifyInventory`, or other Shopify-shaped
domain surface. `CommerceService.SyncProduct` (with the R1 coordination below), operation-key semantics,
`commerce_product_mappings`, the availability formula, and
`orders.OrderService`/`Processor` reconciliation retain their contracts.
Provider-specific GraphQL exists only inside the adapter.

### External identity normalization (Shopify GID ⇄ canonical decimal)

Durable external identities are **canonical decimal Shopify resource
ids** (the GID suffix, identical to the REST-era `legacyResourceId` and
the numeric `id` in webhook payloads), e.g.
`gid://shopify/Product/8294713837621` ↔ `8294713837621`. Reasons:

- the frozen generic tables bound `external_order_id` /
  `external_product_id` to 32 characters (`000013`), and Shopify GIDs
  exceed that bound;
- the frozen order domain canonicalizes external order identity as
  positive decimal (`orders.CanonicalExternalOrderID`);
- webhook payloads expose both forms and Shopify guarantees the GID
  suffix equals the legacy id, so conversion is deterministic both ways
  (documented in `docs/operations/shopify.md`).

GIDs are parsed and **type-checked** at every API boundary (a
`ProductVariant` GID is never accepted where a `Product` GID is
required); GIDs are reconstructed internally before use. Order-line
resolution joins mappings on (provider key, external Product id) —
never SKU.

### Product model and managed variant

One MoonLight Product maps to one Shopify Product with exactly one
MoonLight-managed variant (SKU = MoonLight stable SKU, price = the
configured provider currency converted with exact integer arithmetic to
a decimal string). MoonLight variants are not introduced. Manually
managed Shopify variants are preserved: updates address only the managed
variant (`productVariantsBulkUpdate` with a single id).

`productSet` replacement semantics are used **only for create** (the new
resource has no external state to preserve); every update uses targeted
mutations (`productUpdate`, `productVariantsBulkUpdate`,
`metafieldsSet`), so manual tags, collections, media, SEO, vendor,
product type, and unrelated metafields are never cleared. Category DAG,
MoonLight Tags, images/media, and dimensions are not synchronized
(§40–§43 scope boundaries).

### Ownership metadata

Durable MoonLight remote ownership lives in Shopify metafields under the
dedicated `moonlight` namespace: `product_id`, `provider_key`,
`product_operation_key`, `catalog_revision`, `policy_revision`, and
`managed_variant_id`. **Permanent ownership = MoonLight ProductID +
ProviderKey**; SKU alone is never identity and the operation key is
never identity. Initial creation establishes ownership metadata in the
same remote logical creation (`productSet` create input), so ambiguous
creates and mapping-persistence failures are recoverable without
duplicating remote products (SKU lookup + exact-equality filtering +
ownership match; foreign/multiple candidates conflict).

### Publication-specific visibility

MoonLight manages only the configured
`COMMERCE_SHOPIFY_PUBLICATION_ID` via `publishablePublish` /
`publishableUnpublish`; unrelated sales channels are never touched.
Global product status changes only in the direction Shopify requires
for publication (`DRAFT` → `ACTIVE` when publishing; unpublication never
sets `DRAFT`, which would silently unpublish unrelated channels) —
cross-channel impact documented in the operations guide.

### Safe-zero inventory publication

Call ordering: safe-zero managed inventory → metadata → publication →
`CommerceService.SetInventory` restores frozen `OnlineAvailable`
verbatim. New remote products are created at quantity 0. Only the
managed variant's `available` quantity at
`COMMERCE_SHOPIFY_LOCATION_ID` is ever mutated
(`inventoryActivate` at 0, then `inventorySetQuantities` absolute).

Inventory compare-and-set uses `changeFromQuantity`. Physical correction
keys bind the logical operation key to item identity, observed quantity,
target and inventory-level `updatedAt`. After
every response the physical inventory level is re-read: cached success is
not current stock evidence. Four attempts bound convergence. Retries against
unchanged observed state keep their physical key; a freshly observed level
version gets a new correction key. `CHANGE_FROM_QUANTITY_STALE` and
`COMPARE_QUANTITY_STALE` are retryable; unrelated validation remains permanent.

### R1 coordination and remaining F2 interruption failure

R1 holds a PostgreSQL transaction advisory lock for the entire (provider,
Product ID) sequence: canonical desired-state read, remote writes, mapping
persistence and inventory restoration. Desired state is read after acquiring
the lock. Independent instances serialize through PostgreSQL. The callback
has a two-minute deadline; failure/cancellation releases the lock, and each
Shopify request checks that the database coordination session is alive.
Inventory-only changes are protected by separate remote `inventory_revision`
checks. Remote catalog/policy checks are additional freshness checks, not
atomic conditions on subsequent Product mutations.

This narrowly extends shared `CommerceService` orchestration and the
PostgreSQL repository with an optional coordination capability. Provider
interfaces, schema and dependencies remain unchanged. PostgreSQL-backed
sync serializes both providers, isolated by provider key; Woo adapter
requests retain their existing behavior. Standalone Shopify mutations
require an injected coordinator or a matching coordinated context.

**F2 remains open.** An independent local TLS/PostgreSQL probe received an
old Product update, cancelled its caller (releasing coordination), completed
a newer operation on another instance, then applied the old remote request.
The title regressed. Cancellation cannot retract an already-received remote
mutation. Returning a retryable error is not durable reconciliation. No
unresolved-operation quarantine or durable recovery subsystem is implemented.
Ordinary barrier tests pass, but do not prove this interruption guarantee.
The candidate must not be declared frozen.

### Complete connections and explicit API support

Order lines and fuzzy SKU candidates use complete pagination: 50 nodes per
page, at most 40 pages / 2,000 nodes, one MiB per response and two minutes
per connection. Missing metadata, stalled/repeated cursors, duplicate order
line identities, changed order revision, network failure or bounds exhaustion
fail safely. Partial orders never replace authoritative projections;
incomplete SKU searches never authorize creation. Exact equality and
ownership checks apply after complete retrieval.

Only stable API `2026-10` is supported. Missing/mismatched served
`X-Shopify-API-Version` is rejected. Strict fixtures validate relevant input
shapes and selections, including distinct creation/bulk-update SKU fields.
Official sources:

- [Metafield input](https://shopify.dev/docs/api/admin-graphql/2026-10/mutations/metafieldsSet).
- [Publication input/result](https://shopify.dev/docs/api/admin-graphql/2026-10/mutations/publishablePublish).
- [Bulk variant input](https://shopify.dev/docs/api/admin-graphql/2026-10/input-objects/ProductVariantsBulkInput).
- [Creation variant input](https://shopify.dev/docs/api/admin-graphql/2026-10/input-objects/ProductVariantSetInput).
- [Inventory-level version](https://shopify.dev/docs/api/admin-graphql/2026-10/objects/InventoryLevel).
- [Inventory error codes](https://shopify.dev/docs/api/admin-graphql/2026-10/enums/InventorySetQuantitiesUserErrorCode).
- [API version metadata](https://shopify.dev/docs/api/usage/versioning).

Lineage: `67f479ef8f7dc767ea009d96b5897097fb5c6c17` is the original
Phase 11 candidate. The actual Phase 10 baseline is
`ee58687449d8da737c7ce2708a6decdede7671cd`.

### Order read-only direction and webhooks

Shopify orders enter through the frozen pipeline only:
`POST /api/v1/commerce/webhooks/shopify/{provider_key}` → raw-body
base64 HMAC-SHA256 (client secret, constant-time, before dedupe) +
`X-Shopify-Shop-Domain` binding + `X-Shopify-Webhook-Id` delivery dedupe
on the generic inbox (provider key, delivery id; raw body hashed, never
persisted; no customer PII) → shared lease-fenced processor →
`CommerceOrderProvider.GetOrder` → generic reconciliation with
generation fencing. `orders/cancelled` reconciles via the update path
(cancellation is state, not deletion); `orders/delete` uses
provider-confirmed deletion semantics. Shopify orders never become
Retail Sales/Returns, never mutate inventory, never enter financial
reporting, and never choose the MoonLight Store: Store ownership derives
from product mappings exactly as in Phase 9C.

### No schema change, no Retail change

Generic mappings/orders/webhook persistence already support a second
provider. Migrations `00001–00026` are untouched; `TargetVersion`
stays 26. MoonLightRetail remains at the frozen Phase 10 SHA and knows
nothing about Shopify.

## Consequences

- Woo + Shopify coexist under distinct provider keys with independent
  mappings and isolated failures; the same MoonLight product can publish
  to both.
- Operators must provision a Shopify custom app (token + client secret)
  and configure shop domain, pinned API version, currency, location, and
  publication explicitly; Cloud startup contacts Shopify zero times.
- The pinned API version needs a deliberate quarterly upgrade runbook
  (`docs/operations/shopify.md`); `latest`/`unstable` are refused.
- R1 ordinary cross-instance ordering is improved, but the already-received
  remote mutation interruption failure remains open. No automatic durable
  reconvergence guarantee is claimed.
