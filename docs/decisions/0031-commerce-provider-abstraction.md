# ADR-0031 — Commerce Provider Abstraction (Phase 6A, Cloud)

Date: 2026-09-26
Status: accepted
Scope: Phase 6A (Cloud-only; Retail untouched)

## Context

Phase 5C delivers derived ONLINE availability. The first commerce
adapter (6B) needs a stable provider-neutral boundary to publish
products and synchronize provider-facing inventory without coupling
MoonLight domain code to WooCommerce, Shopify, or any vendor SDK.

## Decision

1. **One interface.** `commerce.CommerceProvider` with `Key`,
   `UpsertProduct`, and `SetInventory` only. Orders, refunds,
   fulfillment, customers, shipping, webhooks, and payments are later
   phases and stay out.
2. **Logical provider identity.** `ProviderKey` is a bounded lowercase
   identifier (`primary`, `website`); never a vendor kind. Adapter
   behavior comes from the registered implementation.
3. **Registry.** Explicit `Register`/`Get`/`List`; duplicates conflict,
   unknown keys are typed not-found, empty registry is legal, startup
   never requires providers or credentials.
4. **Provider-neutral DTOs.** Adapters receive assembled `CommerceProduct`
   snapshots (identities, localized names, exact int64 money, dimensions,
   category/tag refs, lifecycle, policy, revisions) — never sqlc structs
   or database handles.
5. **Availability source.** Provider-facing quantity always comes from
   the frozen Phase 5C availability service; the formula lives in one
   place. Not-ready means safe-zero with the readiness flag retained.
6. **Publication semantics.** `Published = is_active AND sell_online`.
   Unmapped + unpublished is a no-op (nothing remote to disable);
   mapped + unpublished pushes `Published=false` plus quantity 0 while
   retaining the mapping.
7. **Durable mappings.** `commerce_product_mappings` keyed by
   `(provider_key, product_id)` plus unique `(provider_key,
   external_product_id)`, with NO foreign key to rebuildable catalog
   tables so projection rebuilds can never delete them. Same-pair
   creation is idempotent; remaps conflict loudly. Mappings need real
   backup: they are not rebuildable from MoonLight events.
8. **Operation identity.** SHA-256 over canonical length-prefixed state
   (no unordered JSON): product key over provider/product/catalog-rev/
   policy-rev/published; inventory key additionally over
   inventory-rev/quantity. Retries keep the key; state changes alter it.
   Mapping persistence failure stops before `SetInventory`; the stable
   key makes retry duplicate-safe.
9. **Error taxonomy.** Temporary/RateLimited (retryable, optional
   RetryAfter) vs Authentication/Validation/Conflict (terminal).
   Messages must never carry tokens, headers, bodies, or credentials.
10. **No networking in 6A.** Zero external HTTP calls, zero SDKs, zero
    secrets, zero jobs, zero webhooks, zero orders, zero reservations,
    zero Cloud stock mutation, zero dashboard surface. A test-only fake
    provider proves the contract.

## Consequences

- 6B implements one concrete `CommerceProvider`, configures
  credentials outside the database, registers it, and calls
  `CommerceService.SyncProduct`. No domain redesign required.
- 6C extends commerce architecture separately for order ingestion;
  nothing in the product publication interface presumes orders.
