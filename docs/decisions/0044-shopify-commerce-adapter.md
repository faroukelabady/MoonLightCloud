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
domain surface. `CommerceService.SyncProduct`, operation-key semantics,
`commerce_product_mappings`, the availability formula, and
`orders.OrderService`/`Processor` reconciliation are used unchanged.
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

Compare-and-set uses the `2026-04+` `changeFromQuantity` shape:
**zero writes are CAS against the observed quantity** (a stale zero can
never blind-zero a drifted or newer positive quantity), **positive
writes are CAS against the safe-zero this operation establishes** —
an older positive operation can never silently overwrite a proven newer
quantity. Every inventory mutation carries a deterministic Shopify
idempotency key (`@idempotent`) derived from the frozen
`InventoryOperationKey`/`ProductOperationKey`; identical desired state
replays as one remote write.

### Per-product freshness fence (freeze-review F2 remediation)

Concurrent `SyncProduct` operations for one product are ordered by a
**remote freshness fence** carried in the `moonlight` ownership
metafields: `catalog_revision` + `policy_revision` (desired-state
revisions from the frozen source) and `product_operation_key` (the
deterministic operation key — equal desired state always derives the
same key, so a different key always means a genuinely different
operation). No new durable table is required.

Guarantees:

1. **Stale start rejected**: an operation whose desired revisions are
   older than the remote fence aborts *retryably* before any remote
   write (including safe-zero). A stale full sequence running after a
   newer operation completed writes nothing.
2. **Per-child-write compare**: the fence is re-read and compared
   immediately before every child write (safe-zero, content, managed
   variant, fence stamp, publication). A newer operation landing mid-flow
   aborts the stale one at its next write.
3. **CAS-guarded inventory**: zero writes CAS against the observed
   quantity; positive writes CAS against this operation's safe-zero.
   Within our own write ordering a visible newer positive quantity
   always implies a visible newer fence (availability is restored only
   after the fence stamp), so stale zeros/restores cannot land.
4. **Success implies currency**: after the writes, the fence is
   re-verified; supersession (newer revisions or a different operation
   key) fails retryably. The retry re-reads fresh desired state — the
   failure is the durable reconvergence trigger.

Residual (documented, bounded): Shopify exposes no conditional product
mutation in the documented contract (`productUpdate`/
`productVariantsBulkUpdate`/`metafieldsSet` are unconditional), so for
two operations that start against the same remote state and interleave
within one fence check→write round-trip, a single child write can slip
past the loser's check; the loser still fails retryably at its next
stage and its retry reconverges. Hard linearizability across processes
would require a durable per-product generation column (a schema
decision, deferred) or Shopify-side conditional mutations.

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
- Residual concurrency note (freeze-review F2 remediated): concurrent
  `SyncProduct` operations are ordered by the remote freshness fence
  (pre-write stale rejection, per-child-write compare, CAS-guarded
  inventory, post-write supersession verification — see above). The
  bounded residual is a single child write slipping inside one
  fence-check→write round-trip for two same-instant starters; the loser
  fails retryably and its retry reconverges. Hard cross-process
  linearizability would need a durable per-product generation column
  (schema decision, deferred).
