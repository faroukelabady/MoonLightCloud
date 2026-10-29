# Dashboard Operations / Deployment

## What deploys

One OCI image: Node stage builds `dashboard/dist`, Go stage builds the
binary, distroless runtime serves both. No Node at runtime; final user is
`65532:65532`. `DASHBOARD_ASSETS_DIR` points at the baked-in assets
(`/usr/local/share/moonlight-dashboard` in the image).

## Required environment (production)

- `AUTH_MFA_ENCRYPTION_KEY` — base64 32-byte key sealing TOTP secrets
  (required; startup fails without it). Store it in the secret manager,
  never in the database or image.
- `AUTH_SESSION_IDLE_TIMEOUT` / `AUTH_SESSION_ABSOLUTE_TIMEOUT` — optional
  (30m / 12h defaults; bounded).
- The removed `DASHBOARD_USERNAME` / `DASHBOARD_PASSWORD_HASH` /
  `DASHBOARD_SESSION_TTL` variables make startup fail.
- `STORE_TIMEZONE`, `DATABASE_URL`, `DEVICE_SECRET_PEPPER` — as before.
- Session cookies are `__Host-`, `Secure`, `HttpOnly`, `SameSite=Strict`
  outside development (no override). Production must be served over HTTPS.

## Accounts and sessions

Human accounts (OWNER/ADMIN), Store memberships, TOTP MFA, opaque
server-side sessions, CSRF and throttling are described in
`docs/operations/auth.md` and ADR-0053. First deployment: run
`moonlight-cloud auth bootstrap-owner` against the production database.

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

## Overview layout

Overview is the one business page: the former Sales Summary and
Categories & Tags pages are part of it and have no sidebar entries
(`/dashboard/sales`, `/daily`, `/products` and `/categories` open Overview
and the address bar is rewritten to `/dashboard/`).

- Top bar: two rows. The period buttons sit above From/To date fields,
  which always show the applied period's dates and are editable (with
  Show) only for a custom range. The Store selector (Store names; a short
  ID only when two Stores share a name) sits above one All / EGP / USD
  switch that drives every Overview card. The signed-in user is a chip
  with a person icon; Language is blue and Logout red. Every top-bar
  control is one height (`--topbar-control-h`) in both languages.
- Logging out removes `store_id` from the address, so the next login
  starts from all Stores (or the user's own Store). A `store_id` the
  loaded registry does not list also falls back to all Stores.
- Sync health opens the grid at the reading-order start (top right in
  Arabic, top left in English) with a raised border and Refresh in its
  header. Its headline states the real condition: "No shop configured
  yet" (yellow) when no Store is registered, "Needs attention" when any
  sale or return event is blocked, "catching up" while either projection
  is incomplete, and "up to date" only when both are complete with
  nothing blocked. Latest event, backlog, blocked and status are a table
  of sales and returns.
- Sales and profit is one card: a money table (EGP, USD and the total in
  EGP; sales, refunds, net sales, tax when used, cost of goods, net
  profit, average sale) beside the transactions-and-units table, with net
  sales and net profit in the header. `overview.profit` supplies the
  profit: net sales - net tax - net cost (cost of sold lines minus cost of
  returned lines; lines without a cost add nothing), per recorded
  currency and in EGP with every sale and refund converted at its own FX
  snapshot. Sales and returned units follow the currency mode; return
  transactions have no per-currency count and say so in a currency mode.
- Sales by branch and online store is one card: a table with one row per
  branch (sales per currency, refunds, transactions, units; nothing
  converted, no bars), then the finalized-sales vs online-orders
  comparison, which never sums the two.
- Daily trend, top products, categories, top tags, latest sales,
  activity and online orders follow.

Devices lists devices and Stores as tables. Catalog Admin shows each
tab's list beside the selected item's editor (closable), with state
pills, names instead of IDs for the current type and top category, and
prices labelled in piasters / cents.

## Reports page (printable sales report, stocktaking & zakat)

The sidebar's Reports page prints the same sales report as
MoonLightRetail, plus the stocktaking count sheet and zakat calculator.
It uses the header's period and Store; a principal without all-Stores
access must pick a Store (`STORE_SCOPE_REQUIRED`), except that a single
accessible Store is chosen automatically.

- `GET /api/v1/dashboard/reports/sales?currency=EGP|USD|all` returns
  every section (summary, daily, categories, subcategories, tags,
  products, payments, paged Sales details). EGP is the default and shows
  every Sale in EGP: USD Sales use their own FX snapshot. USD shows every
  Sale in USD: an EGP Sale uses the USD rate of the same Store's nearest
  USD Sale (latest at or before it, else the earliest after it), because
  EGP Sales carry no rate (Cloud-entered rates take precedence, see
  Settings). With no rate at all the request
  fails with `EXCHANGE_RATE_REQUIRED` (409). `all` keeps currencies
  apart. Returns use their Sale's conversion.
- The report explains its currencies, as MoonLightRetail's does.
  `currency_mix` totals the filtered Sales per recorded currency, as
  recorded and in the report currency (the dashboard always lists EGP and
  USD). `exchange_rate.latest` is the USD rate in effect when the report
  is made: the newest Cloud rate, else the newest synced USD Sale's
  snapshot in scope (`source` says which). `period_min_microrate` and
  `period_max_microrate` span the rates the period's Sales used; a USD
  Sale's own snapshot always counts, an EGP Sale's rate only when it is
  shown in USD. Each Sales-details row carries `original_currency`,
  `original_total_minor` and the `rate_microrate` that converted it (null
  when shown in its own currency); the page adds a "Recorded as" column
  when any row was converted. Rates change daily, so the page notes that
  older Sales can use a different rate from the latest one.
- Each Sale, line, payment and refund converts on its own and rounds half
  away from zero, so sections can differ by one minor unit. Category, tag
  and product amounts are line totals before sale-level discount and tax.
- `GET /api/v1/dashboard/reports/stocktaking` values every sellable or
  stocked Variant at its EGP cost and price (Variant override, else the
  Product's) from the last synced inventory snapshot.
- Extra costs and zakat inputs live in the page only; nothing is stored
  or sent. Printing uses the browser's print dialog with A4 margins, and
  every section prints whichever tab is open.
- Filters match MoonLightRetail: root categories, subcategories and tags
  (several each) and a payment method keep whole Sales; a Sale stays when
  it has a payment of that method and one line matches every chosen list.
  Returns follow their Sale. The choices list the period's own
  categories and tags.

## Settings page

Business-wide (every Store) and stored in Cloud only:

- Report toggles: tax, discounts, returns. Off hides that feature's rows
  and columns unless the period still holds such amounts; tax off also
  removes tax from sales and refund totals, as Retail does. Saves carry
  the revision that was read (`SETTINGS_CHANGED` when someone saved first).
- Exchange rates: dated USD→EGP rates. A report showing an EGP Sale in
  USD uses the Cloud rate in effect at the Sale (else the earliest), and
  only without any Cloud rate the nearest USD Sale's saved rate. USD Sales
  always keep the rate saved with them.
- Report delivery over WhatsApp and Telegram: see
  [business-reports.md](business-reports.md#dashboard-settings).

Every report reader sees the toggles and rates; changing them, and
anything about report delivery, needs `settings.manage` and all-Stores
access.

## History and custom ranges

Applied filters (period, currency, valid custom ranges) push history
entries. There is no history write on initial load or during popstate
handling, so Back/Forward restores the full filter state without
recursive writes. The custom-range form is
draft/apply: selecting Custom reveals inputs but sends no request until
Apply with two valid dates (from ≤ to); invalid drafts show inline
errors and fire zero API calls.

## Browser E2E test environment

The canonical command runs every spec in `dashboard/e2e/` against a
throwaway **production-mode** Cloud (image built from this tree, fresh
postgres:18.1, random secrets, no default account, clean-slate catalog):

```bash
npx --prefix dashboard playwright install chromium   # once
./scripts/e2e-dashboard.sh                           # whole suite
./scripts/e2e-dashboard.sh e2e/updates.spec.ts       # any Playwright args
E2E_IMAGE=moonlight-cloud:check ./scripts/e2e-dashboard.sh   # reuse an image
E2E_VIEWPORT=1536x1024 ./scripts/e2e-dashboard.sh    # responsive matrix:
                                                     # 1366x768 | 1440x900 | 1536x1024 | 1920x1080
```

Requires podman, node/npx, openssl and curl. What the runner guarantees:

- **Production auth, real HTTPS.** Cloud is served through a test-only
  local TLS terminator (`e2e/support/tls-proxy.mjs`) with a per-run CA and
  leaf certificate, so the real `__Host-mlc_session` cookie (`Secure`,
  `HttpOnly`, `SameSite=Strict`) and the CSRF/Origin checks are exercised
  exactly as in production. Trust is pinned, not disabled: Chromium trusts
  only that leaf's SPKI (`E2E_TLS_SPKI`), Node's request context only the
  run CA (`NODE_EXTRA_CA_CERTS`).
- **Explicit accounts.** The OWNER is bootstrapped through the CLI; the
  Playwright global setup (`e2e/support/global-setup.ts`, active only with
  `E2E_PROVISION=1`) enrolls its MFA and creates + activates + enrolls a
  Store-A-only ADMIN through the real API. Secrets exist only in the run's
  process environment.
- **Single-use TOTP is respected.** The server accepts each TOTP step once
  per account (replay protection). `e2e/auth.ts` (`freshTotp`) records the
  last consumed step per secret and waits — at most one 30 s step — for an
  unused one, so logout/re-login and back-to-back tests never replay a
  code. Expect the full suite to take ~20 minutes for that reason.
- **Test-owned data.** Stores A and B are registered through the device
  API by two provisioned devices. Most specs route-mock dashboard data
  APIs (auth is never mocked). Specs that assert on real report rows ingest
  exactly those sales themselves through the device sync API
  (`e2e/support/device.ts`; e.g. the historical Gold/Golden tag rows in
  `phase12-r1.spec.ts`), with deterministic IDs. Nothing is seeded globally:
  never add demo catalog or sales to migrations or startup to satisfy a test.

Against an already-running stack (e.g. `./scripts/dev-up.sh`), set
`E2E_BASE_URL`, `E2E_DASHBOARD_USER`, `E2E_DASHBOARD_PASSWORD`,
`E2E_DASHBOARD_TOTP_SECRET` (an enrolled account), optionally
`E2E_ADMIN_USER`/`_PASSWORD`/`_TOTP_SECRET`, and
`E2E_DEVICE_CREDENTIAL_A` (a device registered to Store
`aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa`) and run `npx playwright test` in
`dashboard/`. A development-mode server uses the non-Secure
`mlc_session` cookie over HTTP; such a run is a functional run, **not**
proof of production cookie behavior.

Mocked-data specs remain deterministic: fixtures are contract-valid,
period/custom-range flows assert URL/report/input agreement in
`Africa/Cairo` at any wall-clock time, and failure states (503, unsafe
>2^53 money, blocked returns) install their own one-shot routes.

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

## Interface text (2026-10-29)

Dashboard text lives in `dashboard/src/locales/ar.json` and `en.json`, looked
up by key with `msg()` (chosen language) or `msgb()` (the Arabic-mode
"عربي / English" pairing some screens keep). `src/lib/i18n.test.ts` fails when
the two files' keys or `{placeholders}` differ. Adding a language is one more
file plus its `Lang` entry. Text that arrives as data (Store and product
names, labels passed to shared cards) is still chosen per record. The Catalog
Admin page shows only the chosen language.
