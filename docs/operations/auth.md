# Human Accounts, MFA and Recovery (Phase 18 R1)

Architecture: [ADR-0053](../decisions/0053-human-authentication-authorization.md).
There is **no default account and no self-registration** in any environment.

## First deployment

1. **Deploy Cloud** over HTTPS (session cookies are `__Host-`, `Secure`,
   `SameSite=Strict`; the browser will not send them over plain HTTP).
2. **Configure secrets** (secret manager, never the database or image):
   - `AUTH_MFA_ENCRYPTION_KEY` — `head -c 32 /dev/urandom | base64`. Back it
     up: losing it makes every enrolled authenticator unreadable.
   - `DEVICE_SECRET_PEPPER`, `DATABASE_URL`, `STORE_TIMEZONE`, … as before.
   - Optional: `AUTH_SESSION_IDLE_TIMEOUT` (30m), `AUTH_SESSION_ABSOLUTE_TIMEOUT` (12h).
   - Do **not** set `DASHBOARD_USERNAME` / `DASHBOARD_PASSWORD_HASH` /
     `DASHBOARD_SESSION_TTL`: startup refuses them.
3. **Migrate** (`moonlight-cloud migrate up`, schema 42).
4. **Bootstrap the first OWNER** from a shell with database access (a
   one-off container with the same environment is fine):
   ```bash
   moonlight-cloud auth bootstrap-owner --login owner@your-shop.example --display-name "Shop Owner"
   ```
   The password (12–256 characters) is prompted twice without echo; in
   non-interactive jobs use `--password-stdin` or `--password-file`. It
   works once: a second run (or a concurrent one) is refused.
5. **Sign in** at `https://<host>/dashboard/` with that login and password.
6. **Enroll MFA**: add the shown key to an authenticator app, confirm a
   6-digit code, and store the ten one-time recovery codes offline.
7. **Configure Stores and devices** (device provisioning, enrollment as before).
8. **Create the catalog in Retail**: a ProductType, then at least one
   Category (Retail starts with an empty catalog, Retail ADR-042).
9. **Create Products** (or import them; the CSV names the type and root
   category per row).

## Adding people

Users & Security → Add user (OWNER only):

- **ADMIN** operates the shop (catalog, reports, devices, providers,
  releases, rollouts) but cannot manage accounts or security.
- **OWNER** additionally manages accounts and MFA resets. Only an
  all-Stores OWNER can create another OWNER.
- Choose **All Stores** or specific Stores. A Store-restricted human never
  sees other Stores' data or cross-Store aggregates.

The page shows a **one-time activation link** (valid 24 h). Send it over a
trusted channel; the person sets a password and enrolls MFA. "New link"
reissues it (older links stop working).

## Day-to-day security

- Disable a person: Users & Security → Disable (sessions end immediately).
- Role changes and MFA resets end the person's sessions.
- Lost authenticator: the person signs in with a recovery code, or an
  OWNER uses **Reset MFA**; they re-enroll at next sign-in.
- Regenerate recovery codes from *My account* (old codes stop working).
- Throttling: 5 failed sign-ins (or MFA codes) lock that account for 15
  minutes (doubling to 1 h); 30 failures from one network lock it for 15
  minutes. Locks always expire.
- Security audit: Users & Security (all-Stores OWNERs) or
  `GET /api/v1/dashboard/auth-audit`.

## Recovery

- **Forgotten password / lost OWNER access** (server access required):
  ```bash
  moonlight-cloud auth reset-password --login owner@your-shop.example [--reset-mfa]
  ```
  Every session is revoked; with `--reset-mfa` the account re-enrolls.
- **Lost `AUTH_MFA_ENCRYPTION_KEY`**: configure a new key, then run
  `auth reset-password --reset-mfa` for each account.
- The system always keeps at least one active OWNER; a last OWNER cannot be
  disabled or demoted through the UI or API.

## Development

`scripts/dev-up.sh` (and the other scripts) generate a local development
`AUTH_MFA_ENCRYPTION_KEY` into `.env.local` once. Bootstrap a development
OWNER the same way (`go run ./cmd/moonlight-cloud auth bootstrap-owner …`).
Tests create explicit OWNER/ADMIN fixtures; nothing relies on default
credentials.
