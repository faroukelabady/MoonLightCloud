# Threat Model (Phase 1B)

Current mitigations vs future ones are separated explicitly. Nothing below
is solved for features that do not exist yet.

## In scope now

| Threat | Current mitigation |
|---|---|
| Stolen device secret | 256-bit random; HMAC-SHA256 salted verifier at rest; 32B base64 pepper required outside dev; revocation immediate; rotation bounds exposure window |
| Credential leakage in logs | Never log secrets/headers/DATABASE_URL/pepper/reporting tokens; info allowlist only, device_id at debug; tests assert envelope/handler behavior |
| Reporting token exposure | Temporary report-read Bearer, high-entropy (16+ chars), constant-time compare, fail-closed without explicit dev opt-in, never logged, report scope only; device credentials rejected on report routes |
| DB credential leakage | `.env.local` never committed; `.env.example` placeholders; prod refuses dev markers; Desktop never receives DATABASE_URL |
| SQL injection | sqlc parameterized queries + pgx; no string-built SQL |
| Unauthorized device | Bearer `<device>.<credential>.<secret>` verified per request; all failures same 401 without enumeration |
| Revoked device continuing access | Status checked on every auth, no cache; `device revoke` kills all credentials immediately; history preserved |
| Malformed/unbounded payload | 8 MiB body ceiling, sync envelope limits (100 events / 256 KiB), 1 MiB header cap, timeouts, duplicate-key rejection |
| Container running as root | Distroless nonroot (65532), no toolchain/source/secrets in image |
| Public PostgreSQL exposure | Compose binds 127.0.0.1; prod: private connection only, cloud is the sole client |
| Credential rotation failure | Single transaction (never zero usable credentials from partial op); failure before commit keeps old usable; documented re-rotation recovery |
| Credential replay | New secret per rotation; old verifiers revoked server-side |
| Event replay | Idempotent ingestion: identical → already_accepted, altered → 409; replay-safe by construction |
| Event ID reuse / payload substitution | Full immutable-identity compare (device, type, normalized instant, exact stored-payload canonicalization, fail-closed); deterministic EVENT_ID_REUSE; v1 hash never proof of equality |
| Float-precision payload confusion | Exact decimal canonicalization, no float64; distinct large integers never share a hash; hostile exponents rejected before ACK |
| Legacy comparison as attack surface | Duplicate verification re-canonicalizes the stored payload; colliding variants conflict; hash upgrades never mutate payload |
| Auth timing oracle on unknown credential | Fixed dummy HMAC over constant-sized material on unknown-ID paths; credential IDs are high-entropy UUIDs so residual DB-lookup deltas are an accepted LOW note |
| Lost ACK | Retry returns already_accepted; ACK only after commit |
| DB outage during ingestion | No ACK before commit; all-or-nothing batches; 503 retryable |

## Future (documented, not yet mitigated)

- Webhook spoofing (commerce): future signature verification at adapter edge.
- Provider token leakage (Shopify/Meta/Twilio): future secret-manager + allowlist logging rules.
- Pepper rotation: changing `DEVICE_SECRET_PEPPER` invalidates v1 verifiers;
  `pepper_version` rows exist for a future controlled migration; per-request
  signatures/nonces only if threat analysis later demands them.
- Development conveniences that must never reach staging/production:
  `ALLOW_UNAUTHENTICATED_REPORTING` (open reporting) is refused outside
  explicit `ENVIRONMENT=development`, and a missing `ENVIRONMENT` never
  implies open reporting (fail-closed at startup).

## Out of scope for 1B

Dashboard users, customer accounts, payments, commerce data, business
projections — no code, no mitigations claimed.
