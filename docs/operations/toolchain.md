# Pinned Toolchain

Uncontrolled `latest` is not used anywhere. Bump versions deliberately,
one at a time, with a green `check.sh`.

| Tool | Pinned version | Where |
|---|---|---|
| Go | 1.27.2 (`go 1.27.2` in go.mod; security release) | `go.mod`, CI `go-version-file` |
| Build image | `golang:1.27.2-bookworm` | `deploy/Containerfile` |
| Runtime image | `gcr.io/distroless/static-debian12:nonroot` | `deploy/Containerfile` |
| PostgreSQL | `postgres:18.1-bookworm` | `deploy/compose.yaml`, CI service, test helpers |
| pgx | v5.11.0 | `go.mod` |
| goose (library) | v3.28.0 | `go.mod` (migrations run via the app binary; no global goose needed) |
| sqlc | v1.31.1 | `scripts/generate.sh` prerequisite, CI install step |
| govulncheck | v1.8.0 (always `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0`; a `govulncheck` on PATH is never used) | `GOVULNCHECK_VERSION` in `scripts/lib.sh`, CI (v1.1.4 crashes on Go 1.27 sources) |
| uuid | v1.6.0 | `go.mod` |
| GitHub Actions | checkout `d23441a48…` (v6.1.0), setup-go `924ae3a1c…` (v6.5.0) | `.github/workflows/ci.yml` |
| CI runner | `ubuntu-24.04` | `.github/workflows/ci.yml` |
