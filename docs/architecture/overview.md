# MoonLightCloud — Architecture Overview (Phase 1A)

Modular monolith in Go. PostgreSQL is the only state. HTTP is the only
ingress. Everything else is a documented future boundary, not code.

```
                    MoonLightRetail (desktop, separate repo)
                         SQLite, offline-first
                               │ HTTPS (versioned /api/v1)
                               ▼
                        MoonLightCloud (this repo)
                    ┌──────────────────────────────┐
                    │ Go modular monolith          │
                    │  internal/app      wiring    │
                    │  internal/auth     devices   │
                    │  internal/sync     (future)  │
                    │  internal/commerce (future)  │
                    │  internal/notifications (f.) │
                    │  adapter/http      ingress   │
                    │  adapter/postgres  storage   │
                    └──────────────┬───────────────┘
                                   │ private connection only
                                   ▼
                              PostgreSQL 18
                    CommerceProvider ──► future (Shopify/WooCommerce)
                    NotificationProvider ──► future (WhatsApp/Twilio/email)
```

## Boundaries

- `internal/auth` defines repository contracts; `internal/adapter/postgres`
  implements them. Domain packages never import pgx or net/http.
- `internal/commerce` and `internal/notifications` are interfaces + docs.
  The core never imports a vendor SDK (ADR-0007/0008).
- `internal/sync` is documentation only until a later phase.
- Desktop and cloud share no domain package (ADR-0009). The versioned HTTPS
  contract (`api/openapi.yaml`) is the boundary; model duplication is intended.

## Runtime

- Local: Podman Compose (`deploy/compose.yaml`), services `cloud` + `postgres`.
- Production: any OCI host (Railway first, replaceable). The app needs only
  `PORT`/`HTTP_ADDR`, `DATABASE_URL`, stdout logs, SIGTERM handling.
- Stateless app; PostgreSQL holds all authority. No Redis, no broker, no K8s.

## Data

- Fresh MoonLightCloud migration history (goose, embedded, applied
  explicitly). Phase 1A schema: `devices` only.
- UUIDv7 domain identities (app-generated), `TIMESTAMPTZ` UTC timestamps.
- Device secrets: 256-bit random, stored as salted keyed hash
  (SHA-256(salt||secret) or HMAC-SHA256 with `DEVICE_SECRET_PEPPER`).
  Raw secret shown once at provisioning, never logged, never returned.

## HTTP

- Public: `GET /health/live`, `GET /health/ready`, `GET /version`.
- Versioned: device ping, sync capabilities, `POST /api/v1/sync/batches`.
- Stable error envelope `{"error":{"code","message"}}`; no SQL/traces/paths.

## Ingestion vs projection

HTTP commits immutable events to `sync_events` and ACKs; a PostgreSQL-backed
in-process projector derives `sales_projection` (+ lines, payments,
classifications) asynchronously with durable `sync_event_processing` state
(ADR-0016). ACK never waits for projection; restarts recover from the inbox.
- Request IDs assigned/propagated (`X-Request-ID`), bounded client values.
- Timeouts + 1 MiB body/header caps. CORS disabled (no browser client yet).

## Configuration

One typed system (`internal/config`), env-authoritative in production,
fail-fast validation. Production refuses dev DB markers and requires a
secret pepper. See also: docs/operations/railway.md, docs/security/threat-model.md,
docs/sync/protocol.md, docs/operations/backup.md, docs/testing/strategy.md.
