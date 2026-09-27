# ADR-0032 — WooCommerce Adapter (Phase 6B)

Date: 2026-09-27
Status: accepted
Scope: Phase 6B (Cloud-only; Retail untouched, no migration)

## Context

Phase 6A froze the provider-neutral boundary, durable mappings,
operation identity, and error taxonomy. The first real adapter must
publish MoonLight products and availability to WooCommerce without
redesigning that boundary, coupling domain code to a vendor SDK, or
storing credentials.

## Decision

1. **One adapter package.** `internal/commerce/woocommerce`
   implements `commerce.CommerceProvider` against WooCommerce REST API
   v3 (`/wp-json/wc/v3/products`, GET list/get, POST create, PUT
   update). Woo DTOs never leave the package; no SDK dependency.
2. **Transport.** HTTPS-only origins (no userinfo/query/fragment),
   HTTP Basic Auth in the header (never query params), no redirects
   (credentials never cross origins), finite configured timeout,
   1 MiB response bound, context-aware with cancellation preserved,
   classified errors with bounded sanitized messages.
3. **Configuration.** `COMMERCE_WOO_*` env only, disabled by default,
   validated eagerly at startup. Currency explicit (`EGP`/`USD`, no
   silent fallback); dimension unit `cm` only; credentials are
   runtime-only secrets.
4. **Product mapping.** Simple type; publish→`publish`/`visible`,
   unpublish→`draft`/`hidden`; Arabic-primary name/description with
   English fallback; authoritative SKU; exact integer-to-decimal
   prices; cm dimensions or omission; no categories/tags/images
   (omitted fields preserve manually managed Woo state); no cost, no
   sale prices. Ownership metadata (`_moonlight_product_id`,
   `_moonlight_provider_key`, operation key, catalog/policy
   revisions) on every write.
5. **Safe-zero-first.** Every create/update sets
   `manage_stock=true, stock_quantity=0, stock_status=outofstock,
   backorders=no`; `SetInventory` (narrow stock-only payload,
   `instock` iff quantity > 0) restores availability afterwards.
6. **Identity and recovery.** SKU preflight before POST; same-owner
   recovery by update; foreign/multiple → conflict; duplicate-SKU
   POST errors get one bounded recovery lookup; mapped IDs verified
   by GET ownership check (404/mismatch → conflict, no silent
   remap); response IDs must match; Woo IDs normalize to canonical
   decimal in the frozen generic mapping table.
7. **Registry hardening.** Typed-nil providers are rejected without
   panic (6A Low closure), covered by a race-tested registry test.
8. **Operator surface.** `commerce sync-product` CLI calls the frozen
   `CommerceService` once with bounded safe output; no public HTTP
   endpoint, no scheduler, no bulk sync.
9. **Out of scope.** Variable products, taxonomy/image sync,
   multilingual plugins, sale pricing, webhooks, orders,
   reservations, stock writes, dashboard UI.

## Consequences

- 6C adds order ingestion separately; nothing here presumes orders.
- A second Woo instance is another provider key; other vendors are
  new `CommerceProvider` implementations behind the same seam.
