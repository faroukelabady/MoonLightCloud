# Store/device dashboard management

Create Cloud Store records from the Store ID shown by Retail, respecting its
existing identity authority. Create a named device and its Store binding in one
transaction, with a freshly generated credential returned once. Active devices
can rotate credentials (old credentials revoked atomically) or be revoked with
all credentials; revoked devices cannot silently be reactivated.

Use existing devices.manage permission, human session, CSRF and Store scope.
Store creation requires all-Stores access; device actions require the target
Store in the principal's scope. All four actions require a fresh, server-checked,
one-use authenticator code via ConfirmStepUp, not a reusable approval flag.

Show the ready-to-paste device credential only in component memory after create
or rotate, with copy, close and explicit one-time instructions. Clear it on
close/unmount, and do not retain it in local/session storage, URL, history,
server lists, logs or audits. No readback endpoint. Secret-bearing responses
must be no-store. Audit actions using metadata only. No schema changes expected.

Reuse existing auth hashing/provision/rotate/revoke and Store registry SQL.
Preserve rows and history, and reject Store binding conflicts. Add real Postgres
atomicity/authentication tests, protected endpoint tests (permissions, scope,
CSRF, fresh OTP replay/rate limits), UI one-time secret lifecycle tests and
Arabic/English labels. Run canonical gates; second-pass security/UI review.

## Outcome and second-pass review — 9 Nov 2026

Implemented Store creation and bound-device provisioning from the Stores page,
plus credential regeneration and device revocation. All four guarded actions
consume a fresh authenticator code through the same server step-up verifier as
catalog actions. Creating a Store uses Retail's existing Store ID; creating the
device, verifier and binding is atomic. Rotation checks active status inside
the row lock shared with revocation. Existing Store/device rows and history
remain intact; no migration, new dependency or generated SQL edits.

Security/compatibility evidence: real PostgreSQL HTTP tests cover OTP missing,
invalid and replayed codes; CSRF; cross-Store denial; issuance/authentication;
registration using the Retail identity and refusal to rebind; rotation rejecting
the old credential; revocation; no metadata/readback/audit secret disclosure;
and owner-only revocation of unbound legacy devices. Adapter tests reject
missing/disabled Store provisioning without orphan rows. Credential responses
are no-store. The UI retains the secret only until closing/unmounting, with
explicit Copy and one-time instructions; errors clear the OTP.

Validation:
- Canonical `./scripts/check.sh` passed formatting, strict/semantic OpenAPI and
  fixtures, sqlc freshness, disposable PostgreSQL migration, full Go race suite,
  vet, govulncheck, dashboard checks/tests/build/audit and container build. Its
  final clean-tree guard stopped on the expected uncommitted internal source
  changes. Do not describe the full script as passing until committed/rechecked.
- Final dashboard checks: zero errors/warnings, 257 tests passed, build passed.
- Final API/app security tests passed, including same-Store registration
  compatibility. A repeat of the broader PostgreSQL package hit the local
  server connection limit (`53300`) in the existing fleet pause/claim test;
  reducing runner concurrency did not avoid its internal multi-pool demand.
  Final rerun uses a disposable PostgreSQL 18 test server with 200 connections,
  without changing the running Cloud database; the complete PostgreSQL race
  suite passed there. Temporary test containers/databases were cleaned up.

Second-pass source/component security, data, UI, RTL and accessibility review:
no objective unresolved findings. Shared modal supplies initial focus, trap,
Escape and opener restoration; issuance focuses the readonly credential and
errors refocus the OTP. All new visible text has English/Arabic resources;
UUIDs, credentials, timezone and OTP inputs retain LTR direction.

Authenticated browser visual verification was unavailable (preview reached the
login screen only). No live Store, device, business data or credential was
modified. Deploy API and dashboard together after review. Changes remain
uncommitted; operations instructions are in
`docs/operations/store-device-management.md`.
