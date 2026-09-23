# ADR-0023: Cloud Reporting Dashboard Architecture

## Status

Accepted.

## Context

Phase 3B needs an Arabic-first operator dashboard over frozen Phase 3A
reporting without reimplementing business math in the browser, without a
second deployable, and without exposing the machine reporting token.

## Decision

- Svelte 5 + TypeScript + Vite SPA in `dashboard/`, served as static
  assets by the Go server under `/dashboard` (single OCI image, one more
  build stage, no Node at runtime).
- Dashboard BFF routes (`/api/v1/dashboard/...`) reuse `ReportingService`
  directly (no HTTP loopback); dashboard-only presentation values
  (historical-FX normalization) live in `DashboardReportingService`.
- Operator auth is a minimal session boundary: configured username +
  Argon2id hash, HMAC-signed stateless cookie (key derived from the device
  pepper via HKDF, no new secret), HttpOnly + Secure-in-production +
  SameSite=Lax, 12h expiry, per-IP login rate limiting, generic failures,
  same-origin POST checks. `REPORTING_API_TOKEN` never reaches the browser.
- Historical USD→EGP normalization uses each sale's own FX microrate with
  exact integer math (PostgreSQL numeric, explicit bigint casts); native
  Phase 3A buckets flow through untouched.
- No Order lifecycle is fabricated: the orders surface shows latest
  finalized sales (or a future-state note); manual sync is a truthful
  status refresh because Cloud cannot command a Retail outbox push.

## Consequences

One deployable; dashboard math stays server-side; E2E covers login,
periods, modes, toggles, logout. Revisited only by a new ADR.
