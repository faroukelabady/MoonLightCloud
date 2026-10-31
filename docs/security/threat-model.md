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
| Report-delivery misuse from the dashboard (sending business totals to an attacker-chosen number) | Settings mutations need `settings.manage`, an all-Stores principal, a FULL (MFA) session and CSRF; recipients are read back masked (last four characters); provider credentials stay in the server environment and never reach the database or the dashboard |
| A stolen or misused dashboard session changing prices or the catalog in the shop | The shop decides in Retail what Cloud may change (online selling on; product details and prices, and catalog structure off by default) and Retail rejects anything else with no business write. Cloud refuses to queue a change no Store device allows. Product, price and catalog changes need a fresh authenticator code per change (throttled, single-use TOTP step, audited as `auth.mfa.step_up`; recovery codes not accepted). Limitation: online-selling changes need only the FULL session |
| Product image upload abuse (a device filling storage or planting a non-image) | Device auth on every upload; one file of at most 5 MiB per request; the body must hash to its path and decode as JPEG or PNG within 20,000,000 pixels; files are never served publicly: only Cloud itself and signed-in operators with catalog read access to a Store whose galleries name the file (served with nosniff and a sandboxing CSP), and bytes are never logged. Limitation: an authenticated device can still store many distinct valid images; orphaned objects are kept until a later clean-up job (ADR-0056, ADR-0057) |
| Product images sent to Shopify (bytes leaking elsewhere, the access token reaching a third party, MoonLight deleting shop owners' photos) | Stored bytes are re-hashed before upload; the staged target must be HTTPS, redirects are refused and the Shopify access token is never sent to it; only media recorded in `commerce_media_mappings` is reordered, set on variants or deleted, and only on products whose `moonlight` ownership metafields match. Limitation: the staged target host is whatever Shopify returns (not allow-listed); a lost `fileCreate` response leaves the product behind the usual mutation barrier (ADR-0056) |

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

## Phase 11 R1 — Shopify mutation uncertainty

The Phase 1B scope above remains historical. Shopify product synchronization
adds these controls; see [ADR 0044](../decisions/0044-shopify-commerce-adapter.md)
and the [operator procedure](../operations/shopify.md).

| Threat | Mitigation and limitation |
|---|---|
| Cancelled or crashed writer applies remotely after newer intent | Commit an exact-operation barrier before every mutation; active uncertainty blocks all same-provider/Product synchronization across instances and restarts |
| Incomplete response or failed local completion write permits resend | Retain the barrier; neither elapsed time nor a current Product read clears it |
| Late worker or stale operator releases newer work | Completion and resolution bind the operation, provider and canonical Product UUID; operator resolution takes the same sequence lock; replay cannot release another operation |
| Operator incorrectly asserts remote settlement | Resolution requires an explicit closed outcome and confirmation through the privileged local CLI, with durable audit history. This is trusted operator evidence, not an automated provider-status proof; if settlement cannot be established, keep the Product blocked |
| Upgrade or restore discards uncertainty | Quiesce old mutation writers before cutover; back up barriers with commerce state; downgrade refuses retained barrier/history. Mixed-version product mutation writers are unsupported |

Barrier records contain identifiers, a request fingerprint and bounded state,
never credentials or raw request bodies. The design deliberately trades
automatic recovery availability for prevention of uncertain mutation replay.

## R2 image uploads (ADR-0057)

| Threat | Current mitigation and limitation |
|---|---|
| R2 credentials reach Retail or the browser | Only Cloud runtime environment holds static S3 credentials. No presigned/direct-upload URL or public bucket is introduced. Env files are Git- and image-build-ignored. |
| Untrusted uploads consume object storage or select arbitrary keys | Existing device authentication precedes image validation; only bounded JPEG/PNG bytes with matching SHA-256 reach R2. Keys derive from the validated hash. Authenticated-device storage quotas remain future work. |
| Endpoint injection or signed credential forwarding | Require an exact HTTPS Cloudflare R2 account host; reject paths, ports, userinfo, queries and fragments. Refuse redirects; use explicit static credentials with no AWS metadata/default-chain fallback. |
| Partial external/database failure loses an image or creates duplicate identity | Require a successful object write before the PostgreSQL record and ACK, so a recorded file always has an object; retry the same hash/key. Do not compensate with deletion. Failed requests can leave unreferenced private objects. |
| A tampered, truncated or swapped object reaches the dashboard or Shopify | Every read checks the object's size and SHA-256 against the PostgreSQL record and refuses a mismatch (503); reads stop at 5 MiB + 1 byte. |
| Images stored on a deployed Cloud's own disk | `MEDIA_STORAGE=local` is refused outside development; staging/production refuse to start without R2. |
| SDK errors disclose signed request details | Return fixed retryable application errors; raw SDK errors and credentials are neither returned nor logged. Request deadline and two-attempt SDK retry budget bound failure. |

The bucket is the only copy of the image bytes: database backups no longer
contain them. Losing the bucket loses images until a Retail resync re-uploads
the files Retail still holds; keep the token scoped and public access off.
