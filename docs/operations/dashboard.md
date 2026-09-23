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
