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
