# AGENTS.md — MoonLightCloud Engineering Constitution

Tool-neutral. Applies to any engineer or agent working in this repo.

## 1. Architecture boundaries

- Modular monolith. No microservices, no Redis, no brokers, no K8s without an ADR.
- Domain packages (`auth`, `sync`, `commerce`, `notifications`) never import
  `pgx` or `net/http`. Adapters (`adapter/http`, `adapter/postgres`) own I/O.
- `commerce` / `notifications` are interfaces + docs. No vendor SDKs in core.
- Repository contracts live with the domain; implementations in the adapter.
- No shared domain package with MoonLightRetail. The versioned HTTPS
  contract (`api/openapi.yaml`) is the boundary.

## 2. Financial/data safety

- SQL stays visible: sqlc queries reviewed like code. No ORM, ever.
- Migrations are append-only, small, reversible. Never edit applied history.
- Production config refuses dev credentials and insecure defaults. Fail fast.
- Destructive scripts (`dev-reset.sh`) run in development only, against the
  known local DB, with explicit guards. Never improvise drops.

## 3. Database discipline

- Fresh MoonLightCloud history (goose). Migrations run explicitly; startup
  verifies schema version and refuses to boot on mismatch.
- UUID domain identity (app-generated UUIDv7), `TIMESTAMPTZ` UTC timestamps.
- pgxpool with small defaults; per-operation context timeouts; pool closed
  on shutdown. Tests use isolated `moonlight_test_*` databases only.

## 4. Security

- Device secrets: 256-bit random, salted keyed hash at rest, shown once,
  never logged/returned. Constant-time verify. Pepper required in prod.
- Auth failures are enumeration-safe 401s. Revocation is immediate.
- Error envelope only: no SQL, traces, paths, secrets, or driver errors.
- Request IDs bounded; body/header caps and timeouts kept; CORS closed until
  a browser client exists. Threat model updated with behavior changes.

## 5. Testing

- Unit (no DB) + integration (real PostgreSQL 18). No mock-only DB tests.
- `go test -race ./...`, `go vet`, govulncheck are gates, not suggestions.
- Test guards: refuse production env; isolated DBs; parallel-safe.

## 6. Git workflow

- Check `git status` / branch / diff before work. Branch `feat/<scope>`.
- No auto-commit unless asked. Never force-push, rewrite history, stash or
  discard others' work, or `clean` blindly.

## 7. Generated code and migrations

- Never hand-edit `internal/adapter/postgres/sqlcgen`. Regenerate via
  `generate.sh`; `check.sh` fails on stale output.
- Migrations: `db/migrations`, goose annotations, up+down. sqlc parses the
  up sections (verified); keep Down honest.

## 8. Provider-neutral integrations

- No Railway/Shopify/WooCommerce/Meta/Twilio APIs or SDKs in app code.
  Hosting/commerce/notification stay replaceable behind documented seams.

## 9. Review process

- Read-only reviewers per `docs/reviews/*.md`. One line per finding with
  severity. Critical/High block. Reviewers never silently modify code.
