# R2 image upload review — 31 October 2026

Scope: ADR-0057, Cloud configuration, media orchestration, S3 adapter, Compose,
API documentation and tests. Existing dashboard/media branch work is preserved.
No Retail source, schema or live data changes. Feature branch stays uncommitted.

## Findings

No unresolved Critical/High findings in the R2 change after source review.
The deliberate storage duplication and rollout limits are documented, not
represented as a migration of PostgreSQL images.

## Architecture / database / sync

- Confirmed: domain orchestration depends on `media.ObjectStore`; the official
  AWS SDK and HTTP client exist only in `internal/adapter/r2`. Composition owns
  construction. No new public route, queue, daemon, cross-repository domain
  dependency or schema change.
- Confirmed: authenticated image validation precedes R2. R2 success precedes
  the existing PostgreSQL image/commerce transaction and ACK. No network call
  holds a database transaction open.
- Confirmed: original hash and bytes choose a deterministic key. Retried uploads
  overwrite identical content; failures never delete an object that another
  concurrent request might use. DB failure can leave an unreferenced R2 object.
- Confirmed: existing PostgreSQL reads and copies remain in use. Default mode
  and `MEDIA_STORAGE=postgres` rollback need no data reset. Old Retail upload
  bookmarks are retained; existing images need an intentional replay to reach R2.

## Security / Go / deployment

- Confirmed: R2 mode requires explicit static credentials and an exact HTTPS
  account endpoint. Path, userinfo, port, query, fragment and foreign/suffix
  hosts are rejected. The SDK default credential chain is not used.
- Confirmed: redirects are refused; calls have an eight-second total deadline
  and at most two attempts. Context cancellation propagates to the SDK.
- Confirmed: same device authentication, 5 MiB cap, format, dimension, hash and
  dashboard Store checks apply. No public bucket, presigned upload or CORS change.
- Confirmed: SDK failures become a fixed 503 without signed request details.
  Config formatting/JSON omit keys. Only the boolean R2-enabled state is logged.
  No real credential was read, written, printed or used during implementation.
- Confirmed: Compose forwards runtime-only variables. Env files remain ignored
  by Git and image builds. Multi-stage/non-root packaging is unchanged.
- SDK cost: three direct AWS modules plus transitive SDK support modules, used
  for maintained SigV4/retry/S3 compatibility instead of custom signing code.

## Verification

- Focused race tests passed for config, media, S3 adapter, HTTP, app construction
  and PostgreSQL using synthetic credentials and a disposable PostgreSQL 18.
- Authenticated HTTP → real SDK → TLS S3 fixture → real PostgreSQL proof passed.
  It covers failed R2 writes, invalid/unauthenticated input, failed DB commits
  after object writes, same-hash retries and compatible PostgreSQL reads.
- Adapter tests cover signing scope, exact payload/key, duplicate requests,
  transient retry, redirect refusal, terminal 403 and cancellation. Config tests
  cover defaults, missing keys, unsafe destinations, redaction and load wiring.
- Compose service configuration resolves with synthetic variables.
- First broad run: the unchanged catalog mixed-bootstrap test exceeded its
  existing 20-second limit (30/32 events); R2 tests passed. No production catalog
  code or test limit was changed. Three isolated race reruns passed, with maximum
  receipt-to-terminal times of 8.621, 8.646 and 9.606 seconds. A broad rerun is
  recorded separately in `/tmp/moonlight-r2-check.log`.

Live R2 write permission remains unverified until the owner enters the real
credentials and uploads a new image. Service readiness alone is not that proof.

## Final broad-run result

The broad rerun passed all Go tests with `-race` against disposable PostgreSQL
18, Go vet, govulncheck, OpenAPI/fixture parity, generation freshness, dashboard
type/style checks, 187 frontend tests, production frontend build, the required
High-severity dependency audit and the non-root OCI container build. The final
OpenAPI edits were validated again separately.

`check.sh` then stopped at its final clean-tree guard because intentional
feature edits in `internal/` are uncommitted. No Git commit, staging, merge or
push was performed. The SQL-generated files and database schema are unchanged.
The clean-tree guard must be checked after the owner requests a commit; this
record does not claim an overall zero exit status for `check.sh`.

Evidence logs: `/tmp/moonlight-r2-focused.log`,
`/tmp/moonlight-r2-latency.log`, `/tmp/moonlight-r2-check-initial.log`,
`/tmp/moonlight-r2-check.log` and `/tmp/moonlight-r2-openapi.log`.

## Addendum — R2 becomes the only byte store (same day)

Follow-up review found the copy above had no reader: the dashboard and Shopify
still read PostgreSQL, so R2 held an unused second copy and production could
run without R2 (`MEDIA_STORAGE` defaulted to `postgres`). ADR-0057 was
rewritten: `media_blobs.content` is removed (baseline `00114`), reads come
from the object store and are verified against the recorded size and
SHA-256, staging/production require R2, and development uses a local
directory. `.env.example` and the operations guide no longer carry the real
R2 account ID. The PostgreSQL compatibility and rollback statements above no
longer apply.
