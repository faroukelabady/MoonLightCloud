# Baseline Freeze Verification (post-commit)

Run after the Phase 2E remediation is committed. Never validate the final
baseline solely from a dirty development worktree: use a fresh checkout of
the committed remediation commit.

## MoonLightRetail (from a clean checkout)

```bash
git status --porcelain   # must be empty
./scripts/check.sh
go test -race ./...
./scripts/readiness-check.sh
govulncheck ./...
git status --porcelain   # must still be empty
```

Expected: all gates PASS, including the required-source completeness guard
(`internal/sync/secrets/store.go` tracked) and the frontend audit.

## MoonLightCloud (from a clean checkout)

```bash
git status --porcelain   # must be empty
./scripts/check.sh
go test -race ./...
git status --porcelain   # must still be empty
```

`check.sh` covers gofmt, required-files completeness, sqlc freshness,
throwaway-PostgreSQL migration validity, race tests (unit + real PostgreSQL
integration), vet, govulncheck, OCI build, and clean tree.

## Cross-repository E2E (Phase 2E Cloud build)

Rebuild the Cloud image from the committed Cloud tree, migrate, provision a
fresh development device, then from the committed Retail tree:

```bash
MOONLIGHT_E2E_DEVICE_CREDENTIAL=<fresh-dev-credential> ./scripts/cloud-e2e.sh
```

Required: EGP + USD/FX sales, split payments, classifications, offline
recovery, desktop restart, duplicate resend, exact financial projection
equality. Never reuse or print a production credential.
