# Testing Strategy

- **Unit** (no DB): config validation, secret hashing, service logic with
  in-memory repos, middleware/handlers with httptest, error envelope,
  request-ID propagation.
- **Integration** (real PostgreSQL 18): repository roundtrips, migration
  from empty DB, startup schema verification, readiness healthy/sick.
- **Isolation**: `internal/testutil` creates `moonlight_test_<rand>`
  databases per test, migrates, drops on cleanup. Guards refuse
  `ENVIRONMENT=production` and non-test names. Tests skip cleanly when
  `TEST_DATABASE_URL` is unset; `scripts/test.sh` provisions a throwaway
  container automatically.
- **Race**: `go test -race ./...` is the canonical gate (test.sh defaults
  to it; check.sh enforces it).
- **Static**: `go vet ./...`; dependency audit via govulncheck.
- **Manual/canonical**: fresh compose up → migrate → live/ready/version →
  provision → ping → invalid/revoked rejected → restart persistence →
  deliberate reset (see README + §81 checklist in planning).
