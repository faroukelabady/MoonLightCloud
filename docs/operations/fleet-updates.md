# Fleet Updates (Phase 18)

Architecture: [ADR-0051](../decisions/0051-signed-release-registry.md)
(registry) and
[ADR-0052](../decisions/0052-fleet-rollout-device-update-control.md)
(rollouts and device channel). The release signing, key rotation, Retail
bootstrap and manual recovery runbook is MoonLightRetail
`docs/operations/self-update.md`.

## Configuration

| Variable | Meaning |
|---|---|
| `RELEASE_TRUSTED_PUBLIC_KEYS` | Comma-separated base64 Ed25519 **public** keys, the same keys compiled into Retail release builds. Empty or invalid: release import is refused (`RELEASE_TRUST_NOT_CONFIGURED`); delivery of already-imported releases continues. |

- Never put a private key in Cloud: not in the environment, database,
  container image or logs.
- On startup Cloud logs the trusted `key_id`s, never key material.
- Artifact URLs must be `https://`. `http://` is accepted only with
  `ENVIRONMENT=development`.

## Schema

- Migration `00041_fleet_updates` adds these tables:
  - `releases`, `release_artifacts` (signed columns immutable, rows never
    deleted, enforced by triggers);
  - `update_rollouts`, `update_rollout_targets`;
  - `device_update_status`;
  - `update_target_events` (append-only);
  - `update_audit_events`.
- Migration 41 adds the fleet/update schema; the final Phase 18 schema is
  42 because migration 42 adds human authentication (OWNER/ADMIN, MFA).
  Phase 19 adds migration 43 (an additive projection-discovery index), so
  `migrate.TargetVersion` is 43. The down migration drops only Phase 18
  objects.

## Operator workflow

1. **Import:** Dashboard → Updates → Releases.
   1. Select the `release-envelope.json` produced offline.
   2. Enter one HTTPS URL for each signed artifact.
   3. The preview is unverified; the server verifies the signature and
      rejects anything Retail would reject.
2. **Rollout:** Rollouts tab.
   1. Create a draft: release, scope (selected Store, one device, or all),
      Optional or Mandatory, starting percentage.
   2. Start it.
   3. Widen the percentage after the first stage shows SUCCEEDED.
3. **Monitor:** the Fleet tab shows, per device:
   - the reported version and sequence;
   - updater capability (*Not reported* means the device runs pre-Phase 18
     Retail and needs the manual bootstrap);
   - the device's own update state;
   - the current target.
4. **Stop:**
   - **Pause** halts new delivery and can be resumed.
   - **Cancel** stops the rollout permanently and cancels non-started
     targets.
   - **Revoke** (Releases) blocks the release everywhere. Devices that have
     not begun activation abandon it.
   - None of these interrupt a device already installing or checking
     health.

## Failure meanings

| Target state | Meaning / action |
|---|---|
| `PENDING` with a retry time | Retryable failure; Cloud re-offers with backoff (15 min doubling to 12 h, at most 5 attempts) |
| `WAITING_SAFE_BOUNDARY` | A sale or return is open; the update installs when the shop is idle. No action. |
| `ROLLED_BACK` | New release failed health; previous release and data restored. Investigate before re-releasing as a higher sequence. |
| `MANUAL_ACTION_REQUIRED` | Automation stopped deliberately; follow Retail `self-update.md` §8 on that PC |
| `SKIPPED_NEWER` | Device already has a newer release |
| `UNSUPPORTED` | Package-manager/AppImage/Windows install, no artifact for its platform, or a build without release keys |

## Verification and audit

- `GET /api/v1/dashboard/update-audit` lists every import, revoke,
  rollout change, delivery and device report, newest first.
- `GET /api/v1/dashboard/update-targets/{id}/history` lists one device's
  report history.
- The pre-update SQLite backup never leaves the shop PC. Cloud stores only
  states and error codes.
