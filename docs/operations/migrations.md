# Migration and Rollback Policy

Migrations are append-only (`db/migrations`, goose, embedded). Never edit
applied history. Startup verifies the schema version and refuses to boot on
mismatch (readiness fails).

## Downgrade policy

Downgrades are guarded, not silent:

- `00006` Down refuses while any `sync_events.payload_hash_version = 2` row
  exists (v2 exact-hash rows would become unreadable to old code). A v1-only
  database may roll back.
- `00007` Down refuses while any `sale_event_ownership` decision exists
  (durable arbitration must not be casually destroyed). An empty ownership
  table may roll back.
- Guard failures surface as SQL errors (`division by zero` from the
  single-statement guard form — goose splits files on semicolons, so no DO
  blocks are used) and leave schema and data intact.

Production rollback otherwise requires restoring the application version
plus a database backup from before the newer semantics were accepted:

- pre-v2 backup for a 00006 downgrade with v2 rows;
- pre-ownership backup for a 00007 downgrade with decisions.

## Tested paths

`internal/migrate/migrate_test.go` covers fresh→latest, v5→latest,
v6→latest, projection→ownership backfill (winner + loser), v1-only Down,
v2 Down refusal with intact schema/data, and ownership Down policy.

## Telegram recipient bound (00026)

Schema 26 expands recipient checks from 32 to 33 characters in notification
messages, business-report recipients and operational-alert recipients.
Existing values and delivery snapshots are not rewritten. Startup requires
schema 26 for the R1 binary.

Down reinstates the old constraints in one transaction and refuses while
any affected value exceeds 32 characters. A refusal leaves schema version
26 and all data intact. There is no truncation. Rollback requires a compatible
backup/application pair or explicit operator handling of the longer durable
values; do not silently change queued destinations or historical snapshots.


## Phase 13 — schema 28 (`00028_category_online_policy`)

Append-only. Adds three objects in one migration:

- `catalog_categories.online_enabled BOOLEAN NOT NULL DEFAULT TRUE` —
  the Retail-authored, provider-neutral ONLINE channel policy (existing
  rows default to enabled: deployment never suppresses the catalog).
- `catalog_product_online_state` — the ONE canonical effective-online
  eligibility view (shared by commerce publication and Catalog Health;
  providers never traverse the DAG).
- `commerce_product_reevaluations` — the durable, Product-keyed commerce
  re-evaluation queue (see `docs/operations/commerce.md`).

The Down migration is policy-guarded like `00006`/`00027`: it refuses
while any `commerce_product_mutation_barriers` or
`commerce_product_reevaluations` rows or disabled category policies
exist, so a refused rollback leaves the schema untouched (the Phase 11-13
commerce durability stack unwinds as one unit). `TargetVersion` is 28;
startup refuses to boot on mismatch.

## Phase 13-R1 — schema 29 (`00029_commerce_reevaluation_fencing`)

Apply migration 29 explicitly before starting the remediated Cloud. Shipped
migrations 1–28 are unchanged. Migration 29 retains every queued Product,
retry count, diagnostic code and due timestamp, and initializes its requested
generation to 1. Previous due-time leases/backoff become claimable at their
existing horizon. New claims use a separate expiry, opaque UUIDv7 token and
monotonic lease generation; enqueue never clears a live lease.

Rollback to 28 refuses while any reevaluation work exists. Stop workers and
allow durable work to converge before rollback; do not delete pending work to
force a downgrade. An empty queue can be downgraded without truncating or
rewriting any business state.

## Phase 15 — schema 30 (`00030_product_configurations`)

Apply migration 30 explicitly before starting the Phase 15 Cloud. Shipped
migrations 1–29 are unchanged. It adds:

- `catalog_products.configuration_revision` and
  `catalog_product_configurations` — the projected ONLINE product options
  (frame configurations). They carry no SKU and no inventory: the canonical
  Product remains the only stock and identity authority.
- `commerce_product_configuration_mappings` — durable, Store-scoped,
  ownership-keyed provider configuration identity (never label-matched).
- Immutable selection-snapshot columns on `commerce_online_order_lines`
  (`configuration_id`, frame style/colour labels, delta, provider
  configuration id, `configuration_unresolved`).

Existing rows and historical sales/returns are untouched. The Down migration
drops the additive columns/tables; because order-line selections are
immutable history, a downgrade after selections exist is lossy for that
history — take a backup first.

## Phase 15-R3 — schema 31 (`00031_commerce_async_receipts`)

Apply migration 31 explicitly before starting the remediated Phase 15 Cloud.
Shipped migrations 1–30 are unchanged. It extends
`commerce_product_mutation_barriers` with asynchronous receipt columns
(role, request fingerprint, provider operation id, state, product id) plus a
completeness CHECK and indexes for pending/history lookup, so acknowledged
Shopify bundle operations are tracked until they settle.

Down refuses while any receipt exists (including completed adoption
provenance); preserve the mutation-evidence table with the rest of the
durable commerce state and restore a matching backup before downgrading.
