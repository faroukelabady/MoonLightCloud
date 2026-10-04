# Commerce Provider Abstraction (Phase 6A)

Cloud-internal integration boundary for future commerce adapters. No
provider is configured in Phase 6A: the registry is empty in
production and Cloud starts normally.

## Concepts

- **Provider instance**: one logical external storefront, identified by
  a bounded `ProviderKey` (`primary`, `website`). The key is never a
  vendor kind; behavior comes from the registered adapter.
- **Product mapping**: durable `(provider_key, product_id) →
  external_product_id` rows in `commerce_product_mappings`. Integration
  state, not a projection: catalog/policy/inventory rebuilds never
  touch this table, and it requires real database backup (it cannot be
  rebuilt from MoonLight events).
- **Desired state**: assembled from frozen catalog, sales policy,
  Phase 5C availability, and the Phase 13 Category ONLINE policy
  (`catalog_product_online_state` — one canonical eligibility view).
  `Published = is_active AND sell_online AND category_hierarchy_allows_online`.
  When `Published` is false the provider-facing quantity is 0
  (disabled-product semantics); Retail stock authority is untouched.
- **Operation keys**: deterministic SHA-256 retry identities derived
  from provider/product/revisions/publication state (product) plus
  inventory revision/quantity (inventory) plus the Category ONLINE
  policy identity (eligibility fingerprint + policy version). Same
  desired state retried keeps the key; changed state alters it. The
  policy version makes an enabled→disabled→enable cycle produce fresh
  keys, so stale provider idempotency semantics are never reused.

## Durable re-evaluation (Phase 13)

A committed Category policy/hierarchy change (or a Product classification
change) durably schedules every affected Product for re-evaluation:

- `commerce_product_reevaluations` holds one row per Product (primary key
  = coalescing: shared-DAG paths and repeated requests collapse). Rows are
  written **inside the catalog/product projection transaction**, so no
  crash can lose the intent and no provider I/O ever runs inside a
  projection lock.
- A worker claims bounded batches (25) with SKIP LOCKED + 5-minute
  leases and runs `CommerceService.SyncProduct` for **every** registered
  provider. One provider failing never blocks another provider's durable
  progress; the row completes only when all providers converged. Failures
  and unresolved mutation barriers retry with 5s doubling backoff capped
  at one hour (`attempts`, `next_attempt_at`, `last_error_code` are the
  operator diagnostics; barriers are never bypassed, never blindly resent).
- Multi-instance safe; rapid policy toggles converge to the latest
  committed state because the worker re-reads current desired state at
  execution time.

Diagnosis:

```sql
SELECT product_id, reason, attempts, next_attempt_at, last_error_code
FROM commerce_product_reevaluations ORDER BY next_attempt_at;
```

An empty table means the catalog is fully converged toward providers.

## Safe manual diagnosis

```sql
-- Mappings for one product across providers.
SELECT provider_key, external_product_id, created_at, updated_at
FROM commerce_product_mappings WHERE product_id = $1;
-- Reverse lookup: which MoonLight product owns an external ID.
SELECT product_id FROM commerce_product_mappings
WHERE provider_key = $1 AND external_product_id = $2;
-- Provider-facing availability inputs for one product (read-only).
SELECT p.is_active, pol.sell_online, pol.online_allocation_limit,
       inv.stock_quantity, inv.source_revision
FROM catalog_products p
LEFT JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
LEFT JOIN catalog_product_inventory inv ON inv.product_id = p.product_id
WHERE p.product_id = $1;
```

- A mapping conflict means an adapter returned a different external ID
  than the durable mapping holds: the mapping is never silently
  overwritten. Resolve by aligning the remote product, then a newer
  desired state produces a new operation key.
- An unpublished product with no mapping is an intentional no-op: there
  is nothing remote to disable.
- Missing policy or inventory yields provider-facing quantity 0 with a
  not-ready signal, never unlimited availability.

## 6B adapter boundary

A concrete adapter implements `commerce.CommerceProvider`
(`UpsertProduct`, `SetInventory`), reads only the provider-neutral DTOs
it is handed, keeps credentials in adapter configuration (never in the
database, events, Retail, or dashboard), classifies failures with
`commerce.ProviderError`, and is registered under one `ProviderKey`.
Callers use `CommerceService.SyncProduct`. Since Phase 13, a durable
commerce re-evaluation worker also drives it: catalog/product projection
commits enqueue affected Products into `commerce_product_reevaluations`
(atomic with the projection, coalesced per Product), and the worker
converges each Product against every registered provider with bounded
retries (including after unresolved mutation barriers). There is still
no webhook route and no product-level scheduler beyond this queue.

### Phase 13-R1 claim and intent fencing

The durable queue separates requested generation, retry due time and active
claim ownership. Each enqueue advances the requested generation without
clearing the active lease. Completion/retry requires the exact live token,
lease generation and captured requested generation. Expired/stale owners
cannot acknowledge another claim. If newer intent arrived during provider
work, completing the older pass releases that intent for another pass;
retrying the older pass preserves the newer due time and diagnostic state.

An older in-flight provider operation may finish with its captured desired
state. It cannot erase the corrective request: a later pass re-reads canonical
current state through the existing CommerceService. This is eventual
convergence, not remote transactional fencing. Mappings, remote identity,
mutation barriers and provider-specific ambiguity rules remain authoritative.
