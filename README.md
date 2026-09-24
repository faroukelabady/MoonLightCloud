# MoonLightCloud

Always-online integration and analytics layer for MoonLightRetail
(Go + Wails + Svelte desktop, SQLite, offline-first — separate repository,
never modified here). Durable sync inbox plus the first Sale-sync vertical
slice: versioned ingestion (`sale.finalized.v1` and
`sale.return_refund.finalized.v1` validated before ACK),
async PostgreSQL projection with durable processing state, modular Go
monolith, OCI-portable.

## Prerequisites

- Go 1.27.x (pinned in `go.mod`)
- Podman + podman-compose (canonical); `docker compose` compatible
- `sqlc` v1.31.1, `go` (for `go run`-based migrations; no global goose needed)
- PostgreSQL 18.1 container image (pinned in `deploy/compose.yaml`)

## Quickstart

```bash
cp .env.example .env.local   # safe placeholders; edit as needed
./scripts/dev-up.sh          # compose up + wait + migrate + readiness
curl localhost:8080/health/live
curl localhost:8080/health/ready
curl localhost:8080/version
./scripts/dev-provision-device.sh my-shop   # DEVELOPMENT ONLY, credential shown once
AUTH="Bearer <device-id>.<credential-id>.<secret>"
curl -H "Authorization: $AUTH" localhost:8080/api/v1/device/ping
curl -H "Authorization: $AUTH" localhost:8080/api/v1/sync/capabilities
./scripts/dev-down.sh        # stop, keep data
./scripts/dev-reset.sh       # DESTROYS local dev DB only (gated to development)
```

Fast Go iteration (alternative to in-Compose cloud): run only postgres via
Compose, then `go run ./cmd/moonlight-cloud serve` on the host. Canonical
`dev-up.sh` runs both services in Compose to mirror production.

## Commands

| Task | Script (canonical) | Make alias |
|---|---|---|
| Start dev env | `./scripts/dev-up.sh` | `make dev-up` |
| Stop | `./scripts/dev-down.sh` | `make dev-down` |
| Destroy dev DB | `./scripts/dev-reset.sh` | `make dev-reset` |
| Migrate | `./scripts/migrate.sh up\|status` | `make migrate` |
| Regenerate sqlc | `./scripts/generate.sh` | `make generate` |
| Tests (race) | `./scripts/test.sh` | `make test` |
| Full gate | `./scripts/check.sh` | `make check` |
| Provision device (dev) | `./scripts/dev-provision-device.sh [name]` | — |
| Device admin | `device list / rotate <id> / revoke <id>` via `go run ./cmd/moonlight-cloud` | — |
| Projection ops | `projection status` / `projection retry <event-id>` (safe, auditable) | — |
| Dashboard password hash | `dashboard hash-password` reads password from stdin, prints Argon2id PHC | — |

## Dashboard (Phase 3B)

Arabic-first RTL operator UI at `http://localhost:8080/dashboard`
(login: dev defaults `operator` / `moonlight-dev-operator`, dev only).
Frontend lives in `dashboard/` (Svelte 5 + TypeScript + Vite + ECharts);
see `docs/architecture/dashboard.md` (development) and
`docs/operations/dashboard.md` (deployment). The browser uses a session
cookie only — `REPORTING_API_TOKEN` never reaches it.

## Layout

`cmd/moonlight-cloud` · `internal/{app,config,auth,sync,sale,report,commerce,notifications,adapter/{http,postgres},platform/*,migrate,testutil}` ·
`db/{migrations,queries}` · `api/openapi.yaml` · `deploy/{Containerfile,compose.yaml}` ·
`docs/{architecture,decisions,operations,security,sync,testing,reviews}`.

## Configuration

See `.env.example` (placeholders only, never commit secrets):

- `STORE_TIMEZONE`: IANA business timezone (`Africa/Cairo` default in dev,
  explicit + validated in staging/production).
- `REPORTING_API_TOKEN`: temporary report-read Bearer secret. Fail-closed:
  16+ chars enables authenticated reporting anywhere; without a token, open
  reports need ALL of `ENVIRONMENT=development`,
  `ALLOW_UNAUTHENTICATED_REPORTING=true`, and no token — anything else
  (including a missing `ENVIRONMENT`) fails startup.
- `DASHBOARD_USERNAME` / `DASHBOARD_PASSWORD_HASH`: operator login (dev
  defaults only with explicit `ENVIRONMENT=development`; omitted
  environments fail closed).
- `TRUSTED_PROXY_CIDRS`: peers whose `X-Forwarded-For` is trusted for login
  rate limiting (empty trusts none).

## Docs

Start at `docs/architecture/overview.md`, then `docs/decisions/README.md`,
`docs/security/threat-model.md`, `docs/sync/protocol.md`. Engineering rules
in `AGENTS.md`.
