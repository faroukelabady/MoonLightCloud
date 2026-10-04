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
- **Desired state**: assembled from frozen catalog, sales policy, and
  Phase 5C availability. `Published = is_active AND sell_online`.
- **Operation keys**: deterministic SHA-256 retry identities derived
  from provider/product/revisions/publication state (product) plus
  inventory revision/quantity (inventory). Same desired state retried
  keeps the key; changed state alters it.

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
