# Threat Model (Phase 1B)

Current mitigations vs future ones are separated explicitly. Nothing below
is solved for features that do not exist yet.

## In scope now

| Threat | Current mitigation |
|---|---|
| Stolen device secret | 256-bit random; HMAC-SHA256 salted verifier at rest; 32B base64 pepper required outside dev; revocation immediate; rotation bounds exposure window |
| Credential leakage in logs | Never log secrets/headers/DATABASE_URL/pepper; info allowlist only, device_id at debug; tests assert envelope/handler behavior |
| DB credential leakage | `.env.local` never committed; `.env.example` placeholders; prod refuses dev markers; Desktop never receives DATABASE_URL |
| SQL injection | sqlc parameterized queries + pgx; no string-built SQL |
| Unauthorized device | Bearer `<device>.<credential>.<secret>` verified per request; all failures same 401 without enumeration |
| Revoked device continuing access | Status checked on every auth, no cache; `device revoke` kills all credentials immediately; history preserved |
| Malformed/unbounded payload | 8 MiB body ceiling, sync envelope limits (100 events / 64 KiB), 1 MiB header cap, timeouts, duplicate-key rejection |
| Container running as root | Distroless nonroot (65532), no toolchain/source/secrets in image |
| Public PostgreSQL exposure | Compose binds 127.0.0.1; prod: private connection only, cloud is the sole client |
| Credential rotation failure | Single transaction (never zero usable credentials from partial op); failure before commit keeps old usable; documented re-rotation recovery |
| Credential replay | New secret per rotation; old verifiers revoked server-side |
| Event replay | Idempotent ingestion: identical → already_accepted, altered → 409; replay-safe by construction |
| Event ID reuse / payload substitution | Canonical-payload SHA-256 compare on conflict; deterministic EVENT_ID_REUSE |
| Lost ACK | Retry returns already_accepted; ACK only after commit |
| DB outage during ingestion | No ACK before commit; all-or-nothing batches; 503 retryable |

## Future (documented, not yet mitigated)

- Webhook spoofing (commerce): future signature verification at adapter edge.
- Provider token leakage (Shopify/Meta/Twilio): future secret-manager + allowlist logging rules.
- Pepper rotation: changing `DEVICE_SECRET_PEPPER` invalidates v1 verifiers;
  `pepper_version` rows exist for a future controlled migration; per-request
  signatures/nonces only if threat analysis later demands them.

## Out of scope for 1B

Dashboard users, customer accounts, payments, commerce data, business
projections — no code, no mitigations claimed.
