# Testing Strategy

- **Unit** (no DB): config validation, secret hashing, service logic with
  in-memory repos, middleware/handlers with httptest, error envelope,
  request-ID propagation.
- **Integration** (real PostgreSQL 18): repository roundtrips, migration
  from empty DB, startup schema verification, readiness healthy/sick.
- **Isolation**: `internal/testutil` creates `moonlight_test_<rand>`
  databases per test and drops them on cleanup. Each is cloned
  (`CREATE DATABASE … TEMPLATE`) from `moonlight_test_tpl_<hash>`, which is
  migrated once per migration set. The hash covers every migration file and
  `TargetVersion`, creation is serialized by an advisory lock, and the
  template is renamed into place only after verification. Every clone still
  verifies `TargetVersion`. Set `MOONLIGHT_TEST_NO_TEMPLATE=1` to migrate
  each test database from empty instead. Guards refuse
  `ENVIRONMENT=production` and non-test names. Tests skip cleanly when
  `TEST_DATABASE_URL` is unset; `scripts/test.sh` provisions a throwaway
  container automatically.
- **Race**: `go test -race ./...` is the canonical gate (test.sh defaults
  to it; check.sh enforces it).
- **Static**: `go vet ./...`; dependency audit via govulncheck.
- **Contract**: golden `sale.finalized.v1` fixtures mirror the desktop DTO
  semantics exactly (no shared module); validation, projection, snapshot,
  size-boundary, conflict, rollback, replay, and out-of-order tests pin
  compatibility. Drift surfaces as test failure, by design.
- **Manual/canonical**: fresh compose up → migrate → live/ready/version →
  provision → ping → invalid/revoked rejected → restart persistence →
  deliberate reset (see README + §81 checklist in planning).
