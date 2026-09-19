# Railway Deployment Assumptions (future target, not Phase 1A)

Railway hosts the same OCI image built from `deploy/Containerfile`. No
Railway SDK, API, or config lives in the application. Replaceable by
Render / Coolify / Cloud Run / AWS / generic VPS without code changes.

The platform must provide:

- `PORT` (honored when `HTTP_ADDR` is default) or `HTTP_ADDR`
- `DATABASE_URL` (private Postgres connection string)
- `ENVIRONMENT=production`, `DEVICE_SECRET_PEPPER`, optional `LOG_LEVEL`
- TLS termination at the edge; the app serves plain HTTP behind it and must
  respect `X-Forwarded-*` only via the platform's trusted proxy behavior
- `SIGTERM` for deploys/scaling; the app drains within `SHUTDOWN_TIMEOUT`
- Health checks against `GET /health/ready` (traffic) — liveness at
  `GET /health/live`
- Log collection from stdout/stderr (JSON via slog)
- Ephemeral writable filesystem is fine: the app is stateless; PostgreSQL is
  the only authority

No production secrets are configured in this repository. Ever.
