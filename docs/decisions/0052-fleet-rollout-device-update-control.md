# ADR-0052: Fleet Rollout & Device Update Control (Phase 18)

- Status: Accepted (Phase 18).
- Depends on: ADR-0051 (signed release registry). Retail ADR-040/041 are
  the authority for what a device does with a command.
- Scope: staged rollouts, the device update channel, truthful fleet status
  and audit. Cloud orchestrates; Retail decides and executes.

## Context

Operators need to update shops gradually, pause or stop a bad release, and
see each device's real state. The channel must not give Cloud execution
power over devices. Cloud's view must never claim success that Retail did
not report.

## Decision

### Rollouts (migration 00041)

- `update_rollouts` holds:
  - the release;
  - the scope: `DEVICE` (bound, active device in the selected Store),
    `STORE` or `ALL`;
  - the mode: `OPTIONAL` or `MANDATORY`;
  - `percentage` (1–100) and optional `not_before`;
  - the status: `DRAFT → ACTIVE ⇄ PAUSED → COMPLETED | CANCELLED`.
- Creating a rollout snapshots the eligible targets immutably into
  `update_rollout_targets`: active, Store-bound devices in scope, at most
  10,000.
- Each target stores its Store at snapshot time. Delivery requires the
  device's current binding to equal it, so a rebound device never receives
  another Store's rollout.
- **Deterministic staging.** `bucket = sha256(rollout_id:device_id) mod
  100`. A target is selected when `bucket < percentage`, otherwise it stays
  `NOT_SELECTED`.
  - Widening only increases the percentage, over the same snapshot.
  - The result is stable across polls, restarts and Cloud instances.
- **Locking.**
  - Status changes take `FOR UPDATE` on the rollout.
  - Delivery takes `FOR SHARE` on the rollout and `FOR UPDATE` on the
    target, so a pause and a concurrent claim serialize.
  - All time comparisons use the application clock (`@now`), never
    database `now()`.
- **Completion.** An ACTIVE rollout at 100% becomes COMPLETED when no
  target is open. Failed or offline devices never block others.

### Device channel (device credential only)

| Route | Purpose |
|---|---|
| `POST /api/v1/device-control/update/status` | Version, commit, sequence, OS/arch, updater protocol and capability, local state |
| `GET /api/v1/device-control/update/command` | At most one `retail.update.install.v1` command, or `null` |
| `POST /api/v1/device-control/update/targets/{id}/report` | Progress/outcome of the device's own target |

- Device identity comes only from the bearer credential. Bodies are strict
  and never name a device. Another device's target is `404`.
- Revoked devices and operator sessions are rejected (`401`).
- A command is offered only when all of the following hold:
  - the device reported `updater_capable` with protocol ≥ 1;
  - the rollout and the release are ACTIVE;
  - an artifact exists for the reported platform (otherwise the target
    becomes `UNSUPPORTED`);
  - the retry and `not_before` gates have passed.
- The highest release sequence wins.
- The command carries only:
  - the exact signed envelope bytes;
  - the digest, mode and `not_before`;
  - one artifact URL and the release status.
- It never carries an executable command, script, path or arguments.

### Truthful state (`MapReport` / `ApplyReport`)

- Target state is derived only from authenticated Retail reports, using
  Retail's local state names:
  - `SUCCEEDED` only after Retail's confirmed healthy startup;
  - `ALREADY_COMPLIANT` when the release was already installed;
  - `SKIPPED_NEWER` on a downgrade refusal;
  - `UNSUPPORTED` on a platform refusal or missing configuration;
  - otherwise `ROLLED_BACK`, `MANUAL_ACTION_REQUIRED` or `FAILED`.
- Unknown report states are rejected. Terminal states are final, with one
  exception: a CANCELLED target accepts the device's report of real
  activation or its outcome (`target.cancel_superseded`). Cloud never
  claims a cancellation the device did not observe.
- A retryable FAILED report is re-offered with bounded backoff (15 min
  doubling to 12 h, at most 5 attempts). After that it stays FAILED.
- Pause, cancel and revoke never interrupt a device already INSTALLING or
  AWAITING_HEALTH.
- `device_update_status` keeps each device's latest report.
  `update_target_events` is append-only history.

### Operator surface

- All routes are under `/api/v1/dashboard/` with the operator session.
  Mutations require same-origin.
- Fleet and rollout listings are paginated by keyset cursors bound to their
  scope. A cursor issued for one Store filter is rejected for another.
- Every operator and device action writes `update_audit_events`.
- The dashboard "Updates" page has three tabs:
  - **Fleet:** capability, reported state, target.
  - **Releases:** import from an envelope file, revoke, reactivate.
  - **Rollouts:** create a draft, start, pause, resume, widen, cancel,
    per-device targets.

## Consequences

- A compromised Cloud can only schedule or withhold already-signed
  releases. Sequence checks on Retail prevent downgrade.
- Devices that never report (for example Phase 17 Retail, which has no
  updater) appear as *not reported* and are never targeted for delivery.
- Mandatory updates may wait indefinitely for open sales. The fleet view
  shows `WAITING_SAFE_BOUNDARY` rather than a false failure.

## Related documentation

- `docs/operations/fleet-updates.md`
- `api/openapi.yaml`
- Retail `docs/operations/self-update.md`
