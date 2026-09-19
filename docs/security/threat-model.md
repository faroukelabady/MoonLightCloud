# Threat Model (Phase 1A)

Current mitigations vs future ones are separated explicitly. Nothing below
is solved for features that do not exist yet.

## In scope now

| Threat | Current mitigation |
|---|---|
| Stolen device secret | 256-bit random; salted keyed hash at rest; pepper required in prod; revocation immediate |
| Credential leakage in logs | Never log secrets/headers/DATABASE_URL; structured allowlist fields only; tests assert envelope/handler behavior |
| DB credential leakage | `.env.local` never committed; `.env.example` placeholders; prod refuses dev markers; Desktop never receives DATABASE_URL |
| SQL injection | sqlc parameterized queries + pgx; no string-built SQL |
| Unauthorized device | Bearer `<id>.<secret>` verified per request; unknown/id/secret failures all 401 without enumeration |
| Revoked device continuing access | Status checked on every auth; `device revoke` effective immediately |
| Malformed/unbounded payload | 1 MiB body cap, 1 MiB header cap, read/write/idle timeouts |
| Container running as root | Distroless nonroot (65532), no toolchain/source/secrets in image |
| Public PostgreSQL exposure | Compose binds 127.0.0.1; prod: private connection only, cloud is the sole client |

## Future (documented, not yet mitigated)

- Webhook spoofing (commerce): future signature verification at adapter edge.
- Provider token leakage (Shopify/Meta/Twilio): future secret-manager + allowlist logging rules.
- Request replay on mutations: HTTPS + credentials + idempotency keys and
  server deduplication (see docs/sync/protocol.md); per-request
  signatures/nonces only if threat analysis later demands them.

## Out of scope for 1A

Dashboard users, customer accounts, payments, commerce data — no code, no
mitigations claimed.
