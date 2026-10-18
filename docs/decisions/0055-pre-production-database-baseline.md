# ADR-0055: Pre-production database baseline and schema conventions

- Status: Accepted (2026-10-17)
- Relates to: ADR-0012 (explicit migrations, verified schema at startup),
  ADR-0017 / ADR-0019 (payload-hash compatibility), ADR-0039 / ADR-0040 (Store
  scope). MoonLightRetail ADR-049 is the matching Retail decision. Execution
  steps: `MoonLightRetail/plan/database-baseline-plan.md`, Phase 2 and Phase 3.

## Context

No Store runs MoonLight in production. The schema (45 goose migrations) still
carries compatibility for data that never existed:

- `store_id` was added as nullable to `sync_events`, every `catalog_*` table and
  the commerce tables (`00022`–`00037`). 38 places in queries and code handle
  `store_id IS NULL` rows, and several indexes have `WHERE store_id IS NOT NULL`.
- The ingest and projection code accepts 25 event names, including
  `sale.finalized.v1`/`v2`/`v3` and product/category snapshot `v1`/`v2`.
  `payload_hash_version` exists only for the v1 float-based canonicalizer.
  Sale and return projections keep both `fx_rate TEXT` and `fx_rate_microrate`.
- Projections store a currency next to every amount (`unit_currency`,
  `cost_currency`, `line_currency`, `amount_currency`, `change_currency` and
  five on return lines), although all must equal the header currency.
- `operational_alert_recipients` and `business_report_recipients` have the same
  shape. `catalog_admin_device_capabilities` has one boolean column for each
  capability. `device_control_presence.last_seen_at` repeats
  `devices.last_seen_at`.
- Naming differs: `_minor` and `_cents`; UPPER and lower enum values; some ID
  columns are `text` instead of `uuid`.
- Retail ADR-049 also removes the product-level inventory stream and changes
  price, name and frame payloads. The matching projections change here.

## Decision

1. **Pre-production window.** Until the first production install, the event
   contract changes in place, in lockstep with Retail, without new versions.
   Cloud keeps exactly one version of each event type: the version Retail emits.
   Older validators, projectors, parity tests, `payload_hash_version`, the v1
   hash path and `fx_rate TEXT` are deleted.
2. **Store scope is mandatory.** `store_id` is `NOT NULL` with a foreign key on
   every Store-scoped table. Null-handling branches and partial-index predicates
   are removed.
3. **One currency per document.** Sale and return headers keep the currency;
   line, payment and refund rows carry amounts only, plus a CHECK or trigger if
   a row may differ.
4. **Shapes and names.** One `notification_recipients` table with a purpose;
   device capabilities as rows; no duplicate presence columns; `_minor` for all
   money; one enum casing; `uuid` for all identifiers.
5. **One baseline first.** The goose chain 00001–00045 is replaced by
   `00100_baseline.sql` (their Up bodies in order, proven equal with
   `pg_dump --schema-only`). It first checks that `sync_events` does not exist
   and raises an error if it does, so an old database cannot receive it. Each
   later change edits the baseline and raises its version together with
   `migrate.TargetVersion` (00101, 00102, …), matching Retail ADR-049. Every
   development and hosted test database is recreated.

## Owner decisions (2026-10-17)

Shared with Retail ADR-049: localized names use `name_ar` / `name_en` columns
(`catalog_product_translations` is removed); frame options become relational
rows with stable per-product configuration IDs; Retail keeps delivered outbox
rows for ever. Currency Option A is accepted: migration-owned currency rows with foreign keys
on all money-bearing documents.

## Consequences

- Each wire-contract change ships in Retail and Cloud together. Mixed versions
  are not supported during the window.
- Parity and compatibility tests for removed versions are deleted. A test proves
  the migrated schema equals the reviewed target.
- After the first production install, ADR-0012 append-only migrations and
  versioned events apply again without exception.

## Implementation checkpoints

- Baseline 00100 replaced the historical chain with an equivalent schema.
- Baseline 00101 removes payload hash-version metadata, duplicate decimal FX
  text and Tag capture markers. Every accepted Sale is v3 and has captured
  Tags (including an empty list). Exact integer microrates remain the FX
  authority; decimal text is derived for display.
- Category v2 and Product v2 are the only accepted versions of those streams.
  Internal `catalog_*_projection.v1` processor identities remain unchanged.
- Baseline 00102 removes the Product inventory table/event/projector. A read-only
  view sums active, non-deleted canonical Variants only when every such Variant
  has matching ready inventory. No Variant revision/event is presented as aggregate
  Product provenance. Variant-aware commerce waits for at least one Variant; it
  never falls back to publishing an aggregate stock pool.
- Baseline 00103 (with Retail baseline r08, ADR-049 1c) replaces the variant
  EGP/USD price columns with `catalog_product_variant_prices`: per-currency
  price and cost overrides, NULL inherits the Product price. The variant
  snapshot carries them as a `prices` list.
- Baseline 00104 (with Retail baseline r09, ADR-049 1f) stores product
  names and descriptions as `name_ar`/`name_en`/`description_ar`/
  `description_en` columns; `catalog_product_translations` is removed and
  `catalog_tags.name_ar` is required. Category, tag and product snapshots
  carry the same fields instead of locale lists.
- With Retail baseline r10 (ADR-049 1g) frame options are relational
  shared choices in Retail; `catalog.product.configurations.update.v1` is
  removed from Cloud catalog administration. The configuration snapshot
  contract is unchanged.
- Baseline 00105 (step 2c) makes `store_id` NOT NULL on every Store-scoped
  table and drops the NULL-wildcard predicates, raw-default compatibility
  (`default_algorithm`) and the `projection recover-catalog` command.
  Ingest refuses a device without a Store binding (`STORE_ENROLLMENT_REQUIRED`);
  Retail registers its Store before its first upload. Commerce mappings and
  re-evaluations take the Store of their projected Product. Three columns stay
  nullable by design: `update_rollouts.store_id` (a fleet-wide rollout),
  `update_audit_events.store_id` (global audit) and
  `commerce_online_orders.store_id` (orders from a provider not attributed
  to a Store).
- Remaining model steps are tracked in the paired Retail baseline plan;
  this checkpoint does not claim that the whole plan is complete.
