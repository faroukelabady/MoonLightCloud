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
`ee58687449d8da737c7ce2708a6decdede7671cd`; The original Phase 11 schema was 26; the R1 uncertainty amendment below
requires schema 27.

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

### R1 durable mutation uncertainty barrier (F2)

The whole (provider key, Product ID) sequence still holds a PostgreSQL
transaction advisory lock and reads canonical desired state only after
acquisition. Before each Shopify mutation, an independent autocommit
connection stores a UUIDv7 operation ID and request fingerprint in
`commerce_product_mutation_barriers`. The coordinator session is checked
again after that commit and before HTTP. Request bodies, credentials and
provider descriptions are not stored.

Only definitive, well-formed response evidence for the requested mutation
releases its exact operation record. Explicit refusal/validation/throttle
outcomes and proven connect/TLS failure before headers permit release.
Cancellation, timeout, connection loss, 5xx, response-read failure, malformed
or incomplete success, oversized response, or unverifiable served version
retain the record. Uncertainty is independent of the existing ProviderError
retryability classification. No new provider interface or queue is introduced.

R2 amendment (settlement is decided separately from classification): for
top-level GraphQL errors the barrier is retained unless the COMPLETE
response carries validated evidence of a documented pre-execution refusal
— every error entry a uniform `THROTTLED` (cost admission rejects before
execution) or authorization rejection (`ACCESS_DENIED`/`UNAUTHENTICATED`/
`FORBIDDEN`), with **no data key at all** and **no error path on any
entry**. `INTERNAL_SERVER_ERROR`, unknown or missing codes, mixed error
arrays, partial data, `data: null`, and any execution path (or malformed/
contradictory path evidence) all retain the barrier, whatever error the
caller receives. A retryable classification never authorizes removing
durable uncertainty evidence.

`in_flight` and `uncertain` both block subsequent same-provider/Product sync
before any provider call. There is no TTL, lease expiry, startup reset or
same-key automatic replay. A crash may leave `in_flight`; it is deliberately
not assumed safe. Other Products and provider keys remain independent. The
callback has a two-minute bound; retaining durable evidence does not retain
a database connection or advisory lock forever.

Operator recovery uses `commerce product-sync-status` and
`commerce resolve-product-sync`, naming the exact operation UUID and an
explicit confirmed settlement outcome. Resolution acquires the same
canonical UUID advisory lock, is identity-fenced, and retains an audit row.
It is idempotent for the same operation/outcome and conflicts on contradictory
outcomes. It never syncs automatically. A fresh explicit `sync-product` reads
current canonical state after confirmed resolution.

The operator must independently establish that the original remote request
has finished or was not applied. Current Product values, elapsed time,
connection cancellation and a process restart are not settlement evidence.
If that fact cannot be established, leave the Product blocked. Manual
confirmation is a trusted local operator assertion, not an automatic verifier
of Shopify request completion.

This fail-closed availability tradeoff was explicitly approved. The original
received-write cancellation race and actual OCI SIGKILL/restart tests now
prove newer intent is blocked until confirmed settlement, then converges.
F2's former interleaving and interruption paths are covered without claiming
that cancellation can retract a remote request.

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

### Append-only schema amendment, no Retail change

R1 adds only migration `00027_commerce_mutation_barriers.sql` and changes
`TargetVersion` to 27. Migrations 00001–00026 remain byte-identical. The table
has no destructive foreign keys into rebuildable projections. Active barriers
and operator resolution history are durable operational state requiring backup.
Down migration refuses any retained barrier/history; empty-table development
rollback preserves v26 data. No dependency or Retail change is required.

Cutover requires stopping all old product-sync writers and confirming any
legacy in-flight requests have settled before enabling the new writers. Old
writers do not know the new barrier protocol; mixed-version product writes
are not supported. The startup schema check refuses an old `serve` binary
at schema 27, but that check covers `serve` only: older commerce CLI
binaries open PostgreSQL and wire services without a schema check and
without the barrier, so old CLI artifacts, automation and manual mutation
writers must be retired or disabled explicitly. Already-running old writers
must also be stopped. Read-only queries keep
their existing shapes. Do not fabricate uncertainty clearance from a timeout.

## Consequences

- Woo + Shopify coexist under distinct provider keys with independent
  mappings and isolated failures; the same MoonLight product can publish
  to both.
- Operators must provision a Shopify custom app (token + client secret)
  and configure shop domain, pinned API version, currency, location, and
  publication explicitly; Cloud startup contacts Shopify zero times.
- The pinned API version needs a deliberate quarterly upgrade runbook
  (`docs/operations/shopify.md`); `latest`/`unstable` are refused.
- Uncertain Shopify writes deliberately block that provider/Product until
  verified operator resolution. No automatic repair or retry through an active
  barrier is promised. Exact request settlement is an operational prerequisite.

## Phase 15-R3: acknowledged bundle operations

Schema 31 retains bundle receipts on the original mutation-evidence row.
Acknowledgement atomically resolves physical-request uncertainty and records
role, exact request fingerprint and provider operation ID. It does not claim
that the asynchronous operation completed. The same product advisory lock
serializes receipt adoption, polling and subsequent desired-state mutations.
Pending work is polled before any new Product mutation, including a changed
intent. Six reads bound one attempt; durable retries resume the same receipt.
`CREATED` and `ACTIVE` remain pending. `COMPLETE` is inspected for operation
errors and a returned Product; there is no invented `FAILED` provider status.
A lost response retains the original uncertainty barrier. Receipt persistence
failure cannot authorize resend. A completed create receipt remains adoption
provenance after metadata/mapping/publication failures.

Operation errors do not prove that no Product was created. A returned Product
ID is retained even on failure and fences every fresh create until explicit
reconciliation; ordinary retries never erase that evidence or substitute a new
Product. An identical failed create intent is not replayed. A distinct corrected
intent may create only after re-reading the original operation and verifying
`COMPLETE`, operation errors and an explicit null Product. This also covers older
failed receipts which discarded the Product ID: a discovered ID enriches that
failed receipt once without changing its outcome. Missing, malformed, pending
or unavailable operation evidence cannot authorize replacement creation. No
automatic reconciliation or cleanup of already-created remote duplicates is
introduced by this safety fix.

Base, frame component and bundle are separate roles. A mapped bundle resolves
its positively owned base before content or inventory convergence. Only the
base tracks physical inventory; frame variants remain untracked. The configured
publication exposes the bundle for framed Products and the base for simple
Products. Historical configuration identities survive label edits. Bundle
variant adoption requires quantity-one relationships to the managed base and
an owned, configuration-stamped frame variant; titles never establish identity.
Complete product variant reads use bounded pagination (at most 20 pages of
100); incomplete cursors and duplicated variant IDs fail closed.

Rollback to schema 30 is permitted only before any receipt exists. Once a
receipt exists, including completed provenance, downgrade refuses to discard
it. Preserve/backup the mutation evidence with other durable integration state;
projection rebuilds do not delete it. No shipped migration is changed.

Contract references (2026-10):
https://shopify.dev/docs/api/admin-graphql/latest/input-objects/ProductBundleComponentInput
https://shopify.dev/docs/api/admin-graphql/latest/input-objects/ProductBundleComponentOptionSelectionInput
https://shopify.dev/docs/api/admin-graphql/latest/objects/ProductBundleOperation
https://shopify.dev/docs/api/admin-graphql/latest/objects/ProductVariantComponent
