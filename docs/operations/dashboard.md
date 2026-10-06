# Dashboard Operations / Deployment

## What deploys

One OCI image: Node stage builds `dashboard/dist`, Go stage builds the
binary, distroless runtime serves both. No Node at runtime; final user is
`65532:65532`. `DASHBOARD_ASSETS_DIR` points at the baked-in assets
(`/usr/local/share/moonlight-dashboard` in the image).

## Required environment (production)

- `DASHBOARD_USERNAME` — explicit (no default outside development).
- `DASHBOARD_PASSWORD_HASH` — Argon2id PHC from `dashboard hash-password`;
  the dev placeholder is rejected outside development.
- `DASHBOARD_SESSION_TTL` — optional, 15m–168h (default 12h).
- `STORE_TIMEZONE`, `DATABASE_URL`, `DEVICE_SECRET_PEPPER` — as before.
- Cookies are `Secure` in production (HTTP-only behind TLS-terminating
  ingress; the app sets the flag by environment).

## Sessions

Stateless HMAC cookies (key derived from the device pepper via HKDF):
no server-side session store, no Redis. Logout clears the cookie; theft
window is bounded by the short expiry. Rate limiting is process-local
(10 failures / 5 min / IP → 429); multi-instance deployments share no
limiter state (documented limitation, acceptable at this scale).

## Manual sync control

The dashboard "Refresh status" button re-fetches Cloud state only. Cloud
cannot force an offline Retail device to push its outbox; the UI never
claims otherwise. Cloud-side reprocessing stays an operator CLI action
(`projection retry`), preserving auditability.

## Orders surface

There is no Order lifecycle model. The dashboard shows latest finalized
Sales (and a future-state note for order tracking) — never fabricated
New/Preparing/Shipped/Delivered/Refunded states.

## Dashboards vs reporting API

`/api/v1/reports/...` (Phase 3A token auth) is unchanged. Dashboard BFF
(`/api/v1/dashboard/...`, session cookie) reuses the same services.
`REPORTING_API_TOKEN` never leaves the server.

## Reporting failure policy

Dashboard BFF failures share the common envelope:

- `503 UNAVAILABLE` — temporary dependency outage (PostgreSQL
  unreachable, timeouts). Safe to retry; the UI offers Retry.
- `500 INTERNAL` — unexpected internal failure (e.g. aggregate overflow).
  Not retryable as-is; contains no SQL or driver details.

Financial overflow is deterministic internal failure, never
success-with-zero and never 503.

## Trusted proxies

`TRUSTED_PROXY_CIDRS` (comma-separated CIDRs or bare IPs, empty by
default) declares which direct peers may supply `X-Forwarded-For` for
login rate-limit identity. Right-to-left algorithm: from the rightmost
chain entry leftward, skipping trusted proxies; the first untrusted entry
is the client. Untrusted peers and malformed chains fall back to the
direct peer. Never trust ranges you do not operate; arbitrary
`X-Forwarded-For` from the open internet is ignored.

## Queue semantics

`queue_count` is pending + retry counted exactly once. The UI shows it as
"In queue" with the pending/retry sub-breakdown. Blocked events are
terminal conflicts shown separately with allowlisted bilingual labels;
raw stored diagnostics never reach the browser.

## Rounding rule

Historical FX normalization converts each atomic amount (per Sale for
header totals, per Sale line for line metrics) and rounds with
`round(numeric)` (half away from zero) before aggregation. Grouping
(summary vs daily vs branches) therefore cannot change totals.

## Averages

Overview carries per-mode averages (`all`, `egp`, `usd`): truncating
integer division of the mode total by the mode transaction count, computed
server-side and serialized as strings. Absent currencies report zero
transactions with a zero average.

## Daily trend modes

`/api/v1/dashboard/daily?mode=` takes `all` (normalized EGP per Cairo
date, each sale converted with its own historical FX), `EGP` (native
only), or `USD` (native only). Responses carry `mode`,
`display_currency`, and `normalized` so the UI cannot mislabel values.
Products/categories use a separate `all|native` vocabulary; category
kinds are `root_category`/`subcategory` on the wire.

## KPI scope

All Sales-card KPIs follow the active currency mode: transactions, units,
and average come from the mode's server-computed bucket (truncating
division, documented). There is no all-currency fallback while a native
mode is selected.

## History and custom ranges

Applied filters (period, currency, valid custom ranges) push history
entries. There is no history write on initial load or during popstate
handling, so Back/Forward restores the full filter state without
recursive writes. The custom-range form is
draft/apply: selecting Custom reveals inputs but sends no request until
Apply with two valid dates (from ≤ to); invalid drafts show inline
errors and fire zero API calls.

## Browser E2E test environment

`dashboard/e2e/dashboard.spec.ts` runs against the local dev stack
(`./scripts/dev-up.sh`, Go API on `:8080` serving the built dashboard):

```bash
cd dashboard
npx playwright test                                   # default viewport
E2E_VIEWPORT=1536x1024 npx playwright test            # responsive matrix:
E2E_VIEWPORT=1366x768 | 1440x900 | 1536x1024 | 1920x1080
E2E_BASE_URL=http://127.0.0.1:8080/dashboard/ npx playwright test
```

Credentials default to the dev operator
(`operator` / `moonlight-dev-operator`, overridable via
`E2E_DASHBOARD_USER` / `E2E_DASHBOARD_PASSWORD`).

The suite is self-contained: all dashboard APIs are route-mocked with
Phase 4B contract-valid fixtures (net-primary overview, gross/refund/net
daily, net-ranked products/categories, branch rows with refund fields,
sync-health with both error channels). No test depends on developer
database rows, fixed calendar dates, or yesterday/today data — period and
custom-range flows assert URL/report/input agreement against the mocks,
deterministic in `Africa/Cairo` at any wall-clock time. Tests that need
failure states (503, unsafe >2^53 money, blocked returns) install their
own one-shot routes on top of the base mocks.

## Phase 12 — Analytics, Top-N, Online comparison & Catalog Health

### Top Products / Top Categories / Top Tags

Rankings are server-side and deterministic: net line sales (line sales −
line refunds) per currency bucket in a currency scope, units-desc in the
All mode, with complete identity tie-breaks. `limit` (1..100, explicit
validation, default 10 on the widgets) bounds rows AFTER ranking and
never changes ranking semantics. Money stays exact minor units (string
representation; >2^53 renders exactly). Historical sale-time snapshots
are the attribution source: renaming a Product/Category/Tag never
rewrites history.

**Tag totals overlap** (non-additive): one sale line tagged A and B
contributes its full attributable amount to both rows. Tag totals must
NEVER be summed to derive total business revenue. Rows are grouped by
canonical historical Tag identity — the same Tag slug in two Stores stays
two rows.

### Online Orders vs Finalized Retail Sales

The comparison view shows two explicitly separate sources:

- **Finalized Retail Sales** — canonical Sales/Returns financial truth.
- **Online Orders** — operational provider order state (WooCommerce,
  Shopify), read from durable `commerce_online_orders` rows.

Online orders are operational provider orders and are **not**
automatically additional recognized MoonLight revenue beyond finalized
Retail Sales — an online order may later be represented by a finalized
Sale or otherwise duplicate business activity. The two are never summed
into a revenue total. Online money is exact minor units per currency
bucket (no FX). "Active" order value covers exactly
PENDING/PROCESSING/ON_HOLD/COMPLETED; cancelled, deleted, refunded,
failed and unknown orders appear only in the status breakdown. Provider
refunds never become MoonLight Return totals.

### Catalog Health

Diagnostic-only, **read-only**, built exclusively from durable MoonLight
state (current catalog/integration state, never historical Sale
snapshots):

- it does **not** live-query providers (Woo/Shopify/Telegram/WhatsApp);
- it never writes provider or database state, never adopts Stores, and
  never settles/clears mutation barriers (barriers are an
  operator-attention signal only);
- missing mappings are reported only for online-eligible products
  (active + sell_online) — intentionally offline products are not
  failures;
- a mapping never means the remote product is "Published" (labels:
  Mapped / Eligible / Unmapped / Sync ambiguous / Availability not ready);
- provider states are never merged into one flag.

Retail inventory remains authoritative; provider stock is downstream
published state. Stable reason codes (CATALOG_MISSING_SKU,
CATALOG_MISSING_CATEGORY, AVAILABILITY_NOT_READY,
COMMERCE_MAPPING_MISSING, COMMERCE_SYNC_AMBIGUOUS,
COMMERCE_STORE_CONFLICT, and since Phase 17 the variant codes
PRODUCT_NO_ACTIVE_VARIANTS, PRODUCT_NO_SELLABLE_VARIANT,
VARIANT_DUPLICATE_COMBINATION, VARIANT_MISSING_SKU,
VARIANT_MAPPING_MISSING, with VARIANT_INTENTIONALLY_OFFLINE as
informational only) are the API contract; bilingual labels may
evolve. Detail rows are bounded and carry IDs + stable codes only — no
credentials, PII, raw provider errors or SQL.

### Phase12-R1 analytics scope and limits

Top Tags keeps separate Sale-time identities (Tag ID, historical slug and both
historical names). Native currency mode ranks by canonical net line sales; All
mode ranks by units with separate currency buckets. Tag groups overlap because
one Sale line may contribute to several Tags; do not sum them into revenue.

Online order values and every online count/breakdown apply the selected Store,
period, provider and optional currency. Finalized Retail transaction count is
separately labeled as all Retail currencies; its displayed monetary buckets
honor the currency selection. Online values never become Retail revenue or FX.
Overflow fails with a bounded error instead of returning wrapped money.

Catalog Health describes current durable catalog/integration state, independent
of the reporting period. Its summary counts remain complete; detail stays
bounded and explicitly truncated. Provider choices use the same complete durable
provider universe as mapping diagnostics, independent of the selected provider
or first detail page. A specific Store without projected Products does not fall
back to global choices. This is diagnostic evidence, not live provider state:
reads never contact providers, adopt Store ownership or settle mutation barriers.

Top-N limits bound response size, not aggregation work. Canonical Sale/Return
aggregation remains complete before deterministic ranking and truncation. The
current representative fixture assessment is recorded in the remediation report;
this remains a nonblocking scaling observation, not a claim that aggregation is
bounded by the response limit. No cache, background aggregation or new financial
SQL was added.


## Phase 13 — Effective eligibility & Category suppression

Catalog Health eligibility (`COMMERCE_MAPPING_MISSING`, and the readiness
exclusion for `AVAILABILITY_NOT_READY`) now uses **effective** online
eligibility from the canonical `catalog_product_online_state` view:
`active + sell_online + Category hierarchy allows ONLINE`. A Product
intentionally suppressed by Category policy is never reported as
online-eligible-unmapped. Two additions:

- `CATEGORY_ONLINE_DISABLED` — **informational**: an active, sell_online
  Product deliberately suppressed by Category/Subcategory policy (a
  disabled node or ancestor). It is intentional configuration, never a
  catalog-health failure.
- Missing Category hierarchy state keeps reporting through
  `CATALOG_MISSING_CATEGORY` and readiness as before (fail-safe, never
  publishes).

The provider selector and scope context stay visible when the result set
is empty (Phase 12 review item R1-L03, fixed while touching the widget).
Health keeps its frozen invariants: zero provider calls, zero writes,
durable Cloud state only.
