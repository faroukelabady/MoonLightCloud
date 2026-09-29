# Device Connectivity & Remote Sync (Phase 7C)

Retail-initiated control plane: the Retail app polls Cloud over HTTPS;
Cloud never opens connections to Retail.

## Enable the Cloud control plane

```bash
DEVICE_CONTROL_ENABLED=true
DEVICE_COMMAND_LEASE_DURATION=60s   # 10s..10m
DEVICE_ONLINE_WINDOW=60s            # 10s..10m
```

Cloud schema is 18 (migration 00018 enforces the pending/lease
invariant; see below). Migrations 00009–00017 are frozen.

Disabled (default): device-control poll/accept/status routes are 404; the
dashboard device list still reads (all `NEVER_SEEN` until enabled).

## Enable Retail polling

```bash
CLOUD_CONTROL_ENABLED=true
CLOUD_CONTROL_POLL_INTERVAL=15s     # 5s..5m
CLOUD_CONTROL_REQUEST_TIMEOUT=10s   # 2s..60s
CLOUD_CONTROL_MAX_BACKOFF=2m        # 10s..10m
```

Disabled (default): no polling, no background traffic. Retail stays fully
usable offline regardless of Cloud reachability.

## How Online/Offline is derived

`last_seen_at` is Cloud server time from the last authenticated
poll/ack/report. Failed or revoked authentication never updates it.

`last_poll_at` is separate: only real polls refresh it (monotonically);
accepted/running/terminal contact preserves it, and it stays NULL until
the first poll. Dashboards derive connectivity from `last_seen_at` only;
`last_poll_at` is auxiliary poll history.

- `NEVER_SEEN`: no successful control contact ever.
- `ONLINE`: contact within `DEVICE_ONLINE_WINDOW`.
- `OFFLINE`: previously seen, window elapsed.

ONLINE means the Cloud control plane heard from the Retail application
recently. It does NOT prove the PC is healthy, data is current, or every
provider is reachable.

## Offline Sync Now

Requesting Sync Now for an offline (but active) device queues a `pending`
command. The dashboard shows `Queued — waiting for device`. When Retail
reconnects, the same command is delivered, executed once, and reported.

Revoked/disabled devices refuse new commands (`DEVICE_NOT_ACTIVE`); their
polls are rejected and never refresh presence.

## Command states

- `pending`: created, awaiting lease.
- `leased`: returned by a poll, awaiting durable receipt; redelivered after
  lease expiry (same ID, generation+1).
- `accepted`: Retail durably stored the command; polling stops redelivery.
- `running`: Retail started the requested sync cycle (informational).
- `completed` / `failed`: terminal, with a bounded machine result code
  (`SYNC_COMPLETED`, `SYNC_FAILED_NETWORK`, `SYNC_FAILED_AUTH`,
  `SYNC_FAILED_CONFLICT`, `SYNC_FAILED_INTERNAL`). First terminal wins;
  contradictory rewrites return Conflict. A valid identical terminal
  replay succeeds idempotently and refreshes presence (the outcome stays
  immutable); contradictory, cross-device, revoked, and malformed requests
  leave presence unchanged.

## Stuck in accepted

`accepted` means the device owns the command; Cloud will not redeliver.
If the device never reports further, it is offline, crashed, or its worker
is stopped — inspect the device, not the database. Restarting Retail
resumes from the durable inbox (received rows execute, uncertain running
rows safely re-execute, unreported terminals report only). Never delete
rows to retry: issue a new Sync Now after the first command is terminal.

## Restart behavior

- Cloud restart after lease: same command redelivers after expiry.
- Cloud restart after accepted: no redelivery; Retail inbox owns it.
- Cloud restart after terminal commit: duplicate reports are idempotent.
- Retail restart before local commit: redelivery after Cloud lease expiry.
- Retail restart after local commit: command survives; ack/report retry
  continues and execution proceeds.
- Retail restart during execution: safe at-least-once re-run of the
  idempotent sync cycle.
- Retail restart after terminal commit: report only, no re-execution.

## Result codes

Machine codes only, max 64 chars. Raw errors, HTTP bodies, credentials,
SQL, and customer data are never persisted, logged, or returned.

## Backup requirements

`device_control_presence` and `device_control_commands` are durable
operational state: back them up. No rebuild deletes them; no retention
purge exists. The Retail `cloud_control_commands` inbox is durable until
its terminal result is acknowledged by Cloud.

## Pending/lease integrity (schema 18)

A `pending` command never carries `lease_until`: the lease is set at
claim time when the row becomes `leased`. Migration 00018 adds the named
constraint `device_control_commands_pending_lease_null` enforcing this.
If the upgrade aborts naming that constraint, legacy pending rows with
leases exist: reconcile them first (let leases expire and converge, or
drive the commands terminal), then re-run the upgrade. The failed
upgrade rolls back; no rows are deleted and terminal history is never
rewritten.

## Dashboard reads

The device list uses a fixed number of batched reads (metadata without
credentials, presence, active commands, five newest commands per device)
regardless of device count. History per device is bounded to five newest
(`requested_at` DESC, command ID tiebreak). Missing/invalid
Idempotency-Key values are rejected with 400
`DEVICE_COMMAND_INVALID_IDEMPOTENCY_KEY`; unknown devices answer 404.
