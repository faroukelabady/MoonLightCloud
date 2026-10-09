# ADR-0053: Cloud Human Authentication & Authorization (Phase 18 R1)

- Status: Accepted (Phase 18 R1).
- Supersedes: the Phase 3B single-operator login (`DASHBOARD_USERNAME` /
  `DASHBOARD_PASSWORD_HASH`, stateless HMAC cookie, development default
  credential). Those variables now fail startup.
- Scope: humans using the dashboard/Admin APIs. Retail device credentials
  (`internal/auth`), the report token API and signed provider webhooks are
  unchanged and remain separate identities.

## Context

The Cloud dashboard controls catalog commands, devices, provider
integrations, signed release imports and mandatory fleet rollouts. One
shared operator login with a development default, no roles, no Store
scoping, no MFA and non-revocable sessions is not acceptable for that power.

## Decision

### Identity

`admin_users` (migration 00042) holds explicitly provisioned humans:
normalized unique login, display name, role `OWNER|ADMIN`, status
`PENDING_SETUP|ACTIVE|DISABLED`, `all_stores` or explicit
`admin_user_store_memberships`. Humans are never Stores and never devices.
There is no registration endpoint and no default account in any
environment.

### Bootstrap and recovery (server-side only)

- `moonlight-cloud auth bootstrap-owner --login L --display-name N
  [--store ID]…` creates the first OWNER exactly once: an exclusive table
  lock, an "no humans yet" check and a singleton `admin_bootstrap_state`
  row make concurrent or repeated bootstrap fail. Omitting `--store` grants
  all-Stores access (no Stores exist at first deployment).
- `moonlight-cloud auth reset-password --login L [--reset-mfa]` is the
  recovery path; it requires server/database access, revokes every
  session, and with `--reset-mfa` forces re-enrollment.
- Passwords come from the terminal without echo (Linux), stdin or a file —
  never command-line arguments.
- An OWNER creates further accounts (`PENDING_SETUP`) and conveys a
  one-time activation link (256-bit token, SHA-256 digest at rest, 24 h,
  single-use under concurrency, carried in the URL fragment). The user sets
  a password and enrolls MFA; only then is the account ACTIVE.

### Authorization

Named permissions mapped centrally from roles (`humanauth.RoleHas`):

| Permission | OWNER | ADMIN |
|---|---|---|
| catalog.read / catalog.manage, reports.read | ✓ | ✓ |
| devices.read / devices.manage, operations.read / operations.manage | ✓ | ✓ |
| providers.manage, releases.read / releases.manage, rollouts.read / rollouts.manage | ✓ | ✓ |
| users.read / users.manage / security.manage | ✓ | — |

Every `/api/v1/dashboard/*` route is registered through `HumanAuth.Guard`
with a permission and a Store scope; a route-coverage test extracts every
registered route and proves anonymous, device-credential and pre-MFA
callers are refused.

Store scope is enforced server-side from CURRENT memberships (read per
request, so removal is immediate):

- `ScopeQuery` — `store_id` must be a member Store; an empty `store_id`
  (aggregate across Stores) needs an all-Stores human;
- `ScopeHandler` — the handler resolves the owning Store (catalog command
  body, device binding, rollout Store; ALL-scope rollouts need all-Stores);
- `ScopeAllStores` — cross-Store resources (release import/revocation,
  operations incidents, target history);
- releases are global infrastructure: reading needs `releases.read`;
- device and Store lists are filtered to member Stores.

OWNER safety: the last active OWNER can never be disabled or demoted; the
service locks every active OWNER row (`FOR UPDATE`) inside the change
transaction, and an `admin_users` statement trigger is the backstop.
A Store-restricted OWNER manages only users within its Stores and cannot
grant all-Stores access; only an all-Stores OWNER mints new OWNERs. Users
are disabled, never deleted (audit integrity).

### Passwords

Argon2id PHC (`m=64 MiB, t=3, p=2`, 16-byte `crypto/rand` salt, 32-byte
key); weaker stored parameters are rehashed on successful login
(compare-and-set). Policy: 12–256 Unicode code points, ≤1024 bytes, valid
UTF-8, no composition rules, no silent truncation. Malformed hashes fail
closed. Unknown accounts still pay one Argon2 verification.

### Sessions and CSRF

- Opaque 256-bit random token in one cookie: `__Host-mlc_session`,
  HttpOnly, Secure, SameSite=Strict, Path=/ (development: `mlc_session`
  without Secure, development mode only, no override). The database stores
  only SHA-256(token).
- Stages: `MFA_PENDING` / `MFA_SETUP` (10-minute challenge sessions that
  reach only MFA endpoints, `/me` and logout) → `FULL`. Rotated (old row
  revoked, new token) after login, MFA completion, enrollment and password
  change; a rotation fails if the old session was already rotated.
- Idle 30 min and absolute 12 h by default (bounded config); expired,
  revoked, disabled-user and security-version-stale sessions are rejected.
  Password/MFA/role/status changes bump `security_version`, invalidating
  existing sessions.
- Logout revokes server-side.
- CSRF: SameSite=Strict + mandatory same-origin Origin/Referer + a
  per-session token (HMAC of the session ID under a pepper-derived key,
  digest stored) sent as `X-CSRF-Token` on every mutation, kept in memory
  by the SPA (never browser storage). Device routes have no CSRF semantics.

### MFA

RFC 6238 TOTP (HMAC-SHA1, 30 s, 6 digits, ±1 step; implemented in-house
over RFC 4226/6238 test vectors — no new dependency). Mandatory for OWNER
and ADMIN. The secret is AES-256-GCM sealed with `AUTH_MFA_ENCRYPTION_KEY`
(outside the database; the user ID is bound as associated data; required
in every environment, startup fails closed). A step is accepted once
(`last_used_step` compare-and-set). Ten 80-bit recovery codes are shown
once, stored as SHA-256 digests, single-use under concurrency, and
regenerable (old codes invalidated, audited).

### Throttling

PostgreSQL-backed, on the application clock: per account (5 failures /
15 min → 15 min lock doubling to ≤1 h) and per network identity (30 /
15 min → 15 min). MFA failures count. While locked, even a correct
password is refused (429); unknown accounts throttle identically, so 429
reveals nothing. Locks are always temporary; stale rows are pruned.

### Audit

Append-only `auth_audit_events` (trigger forbids update/delete), separate
from update audit: login success/failure category, MFA verify/enroll/
recovery use/regeneration/reset, password change/reset, bootstrap, user
created/activated/role/status/memberships, session revocation, and
authorization denials on mutations. Never passwords, TOTP secrets/codes,
recovery codes or tokens.

### Not in this phase

OAuth/OIDC/SAML/social login, WebAuthn, email delivery, shopper accounts.
Step-up re-authentication ("recent MFA" for release management, mandatory
rollouts and user/security changes) is deferred to Phase 20 hardening;
those actions currently require a FULL MFA session plus explicit UI
confirmation.

## Consequences

- Deployments must provision `AUTH_MFA_ENCRYPTION_KEY` and bootstrap an
  OWNER (`docs/operations/auth.md`).
- Losing the MFA encryption key makes every TOTP secret unreadable:
  recovery is `auth reset-password --reset-mfa` per user.
- Restricted (single-Store) humans cannot see cross-Store aggregates,
  operations incidents or target history — deny by default.
