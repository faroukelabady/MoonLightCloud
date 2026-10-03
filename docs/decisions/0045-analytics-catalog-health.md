# ADR-0045: Analytics, Catalog Health & Reporting Closure (Phase 12)

Date: 2026-10-03
Status: accepted (Phase 12 implementation; freeze review pending)

## Context

Phase 11 froze Shopify as a second commerce provider on the canonical
CommerceProvider/CommerceOrderProvider architecture. The remaining
roadmap item (8I — Dashboard & Reporting Improvements) asked for Top
Products/Categories/Tags, a truthful online-vs-store comparison, and
catalog-health indicators. Most reporting already existed (Phase 3/8/9:
summary, daily, product/root-category/subcategory/tag/cashier/channel
breakdowns, refunds mirrors, Store scoping, historical snapshots). This
phase fills only genuine gaps and closes the inherited Phase 11-R3 Low
F12 (catalog concurrency tests mishandling permitted PostgreSQL
serialization aborts).

## Decision

### Analytics source of truth (frozen)

Financial analytics derive ONLY from finalized Sales, Returns/refunds and
their historical sale-time snapshots (sale_lines_projection,
sale_line_classifications_projection, sale_item_tag_snapshots). No
financial number is ever computed from current Products/Tags/Categories,
provider orders, or provider inventory. Existing queries remain the
single ranking authority: no second Product/Category/Tag financial query
was created.

### Top-N ranking semantics

An optional, additive `limit` (1..100, explicit validation — never
silent clamping) bounds breakdown rows AFTER the existing deterministic
ranking: net line sales (line_sales − line_refunds) per currency bucket
in a currency scope; units-desc in the all-currency scope (cross-currency
net is undefined — EGP and USD are never summed). Tie-breaks are the
existing complete identity comparators (product id/SKU/name; category
kind/id/names), so equal rows never shuffle across reruns. Negative net
values rank last but are never clamped: canonical financial truth.

### Tag non-additivity

Tag attribution is historical, overlapping and non-additive: one sale
line tagged A+B contributes its full attributable amount to both rows
(Phase 8 semantics). Rows group by canonical historical Tag identity
(tag_id + snapshot labels), never merged by slug across Stores (Phase 9
Store-local Tag identity). Every Top Tags surface renders the mandatory
disclosure: "Tag totals overlap and must not be summed to derive total
business revenue." A sum-of-tags revenue figure is never displayed.

### Online orders vs finalized Retail Sales

Provider online orders (WooCommerce + Shopify) are operational commerce
truth and are NEVER combined with canonical Retail financial totals: an
online order may later become a finalized Sale or otherwise duplicate
business activity. The comparison surface shows the two sources side by
side with explicit source labels and a double-count warning. Operational
metrics: order count, order value (exact minor units per currency
bucket), status distribution, provider, Store scope, period. "Active"
value covers exactly PENDING/PROCESSING/ON_HOLD/COMPLETED; cancelled,
deleted, refunded, failed and unknown orders appear only in the status
breakdown. Provider refunds never become MoonLight Return totals.

### Catalog health definitions

Health is a read-only diagnostic over durable MoonLight state (current
catalog/integration state — never historical snapshots). Exact
predicates live in `db/queries/catalog_health.sql`:

- CATALOG_MISSING_SKU — projected SKU absent/blank. Legacy stored SKUs
  are preserved (never regex-rejected: Phase 8A legacy preservation).
- CATALOG_MISSING_CATEGORY — required root/top category absent or
  unresolved. Optional subcategories and hidden categories are not
  failures.
- AVAILABILITY_NOT_READY — online-eligible (active + sell_online)
  product without a projected inventory row (frozen readiness = product
  + policy + inventory).
- COMMERCE_MAPPING_MISSING — online-eligible product without a mapping
  for a known provider. Intentionally offline/inactive products are
  never flagged.
- COMMERCE_SYNC_AMBIGUOUS — an unresolved Phase 11 mutation barrier
  exists (needs operator settlement).
- COMMERCE_STORE_CONFLICT — durable mapping Store ownership differs
  from the catalog product's Store.

### Provider health evidence

Labels are evidence-based (Mapped / Eligible / Unmapped / Sync
ambiguous / Availability not ready). A mapping never implies remote
"published" state. Provider states stay separate (never one merged
flag); the durable provider universe is provider keys observed in
mappings or barriers; a narrowed provider filter never falls back to
all. Missing mapping is normal for intentionally offline products.

### No live provider reads, no mutation from health

Catalog Health endpoints never contact Woo/Shopify/Telegram/WhatsApp,
never write, never adopt Stores, and never settle/clear/retry Phase 11
mutation barriers. The barrier is read strictly as an "operator
attention" signal. Retail inventory remains authoritative; provider
stock is downstream published state.

### Store/currency/date scope

All financial rankings obey the Phase 9 scope model (global = ALL +
legacy; specific Store excludes other Stores and legacy NULL; no global
fallback on errors). Currency buckets stay separate (no FX). Dates reuse
the frozen Cairo/report-period semantics. Online-order analytics use
`commerce_online_orders.store_id` with the same scope rules. Catalog
health follows current-state Store ownership.

### Exact money

New surfaces serialize money as exact minor-unit strings (the
established dashboard representation); int64 values beyond 2^53 render
exactly through the existing BigInt formatting and safe-magnitude chart
helpers. No float, no JS-number money.

### F12 test-retry semantics (architecture-level)

Catalog projection production semantics: projections run in
SERIALIZABLE transactions with advisory entity locks; SQLSTATE 40001
(serialization_failure) / 40P01 (deadlock_detected) are classified
transient by `isSerializationFailure`, and `persistCatalogRetry`
commits durable retry state returning
(OutcomeRetryable, transient error). There is NO in-process production
retry: rows are rediscovered by scan. Tests now model exactly that
contract through `catalogAttemptRetryable`: the durable-retry pair or a
production-classified serialization abort is re-attempted within a
finite budget (exhaustion = test failure with SQLSTATE diagnostics);
every other error fails immediately. Race pressure and all semantic
assertions are preserved.

## Consequences

- Rankings are bounded and deterministic; limits never change semantics.
- Online-order analytics stay operational forever unless a future phase
  defines an explicit accounting relationship.
- Catalog Health is safe to poll: zero provider traffic, zero writes.
- The report breakdown remains the only financial ranking query; query
  plans at larger scales are Phase 13 performance candidates.
