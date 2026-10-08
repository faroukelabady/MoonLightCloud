# Migration and Rollback Policy

Migrations are append-only (`db/migrations`, goose, embedded). Never edit
applied history. Startup verifies the schema version and refuses to boot on
mismatch (readiness fails).

## Schema 40 development rollback and reapply

Shipped migration `00040_create_command_identity.sql` is unchanged. Its Down
restores the schema-39 entity-length check under `catalog_admin_commands_entity_check`,
while its Up expects the original `catalog_admin_commands_entity_id_check` name.
Before a schema-39 upgrade crosses 40, the migration runner verifies the exact
validated, local, single-column `char_length(entity_id) BETWEEN 1 AND 64` check,
the absence of schema-40 identity columns, and the current maximum Goose version.
It renames only that equivalent rollback constraint inside a locked transaction.
It does not drop or weaken the check or rewrite command data. Missing, ambiguous,
unvalidated or altered checks fail with a bounded diagnostic and leave the schema
unchanged; inspect those discrepancies rather than forcing an upgrade.
An upgrade starting below 39 reaches the schema-39 boundary first, so a deeper
development rollback followed by reapply receives the same guarded repair.

The existing Down policy remains development/test only: create-kind commands
and their targets are removed by the shipped Down, while entity commands and
their targets survive down/reapply. This compatibility repair does not make
production rollback safe or change its backup requirement.

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

## Phase 17 — schemas 33 and 34 (`00033_product_variants`, `00034_commerce_order_variant_snap`)

Apply both migrations explicitly before starting the Phase 17 Cloud. Shipped
migrations 1–32 are unchanged. Migration 33 moves SKU/inventory ownership
from the Product to the ProductVariant projection:

- `catalog_product_variants` — variant identity, SKU, `is_active`,
  tombstone `deleted`, per-currency price overrides, `combination_key`,
  variant/catalog revisions, Store-scoped uniqueness (legacy `store_id NULL`
  never collides, per `00023`). Deliberately **no FK** to `catalog_products`
  (rebuildable projection root, `00012` style); the projector enforces the
  product dependency as a retryable wait.
- `catalog_product_variant_attribute_values` and
  `catalog_product_variant_inventory` — child rows FK-owned by the variant
  root, bilingual labels and per-variant stock with its own
  `inventory_revision` gate.
- `commerce_product_variant_mappings` — durable provider identity
  (provider + Store + product + variant + external ids), no FK to
  rebuildable projections.

Variants are tombstoned, never hard-deleted. Migration 34 adds immutable
order-line variant snapshots (`variant_id`, `variant_sku`,
`variant_attribute_snapshot` JSONB) captured at ingestion; they carry no FK
so they survive catalog rebuilds and tombstones. Existing rows and
historical sales/returns are untouched.

`catalog_products.sku` becomes the deprecated display mirror; uniqueness and
authority live in `catalog_product_variants`. `TargetVersion` is 34;
startup refuses to boot on mismatch. The Down migrations drop the additive
tables/columns; because order-line variant snapshots and tombstones are
immutable history, a downgrade after variant data exists is lossy for that
history — take a backup first.
