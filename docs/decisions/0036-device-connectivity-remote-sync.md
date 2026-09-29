# ADR-0036: Device Connectivity & Remote Sync (Phase 7C)

## Context

Cloud needs truthful Retail connectivity and a way for operators to request
one real synchronization cycle from the dashboard. Retail may sit behind NAT
with intermittent internet; Cloud must never dial Retail directly.

## Decision

Retail-initiated short polling over authenticated HTTPS (no WebSockets, no
broker, no reverse tunnel). Correctness comes from durable state on both
sides plus idempotent redelivery, not from persistent connections.

- **Identity:** reuse Phase 1B device ID + credential + revocation. No
  second registry, no new token system. Device routes require device auth;
  dashboard routes require the operator session; cross-auth is rejected.
- **Connectivity truth:** `device_control_presence.last_seen_at` is written
  from Cloud/database server time on every authenticated poll/ack/report.
  Failed auth never touches presence. `NEVER_SEEN` / `ONLINE` / `OFFLINE`
  derives from last-seen plus `DEVICE_ONLINE_WINDOW`. ONLINE means "the
  control plane heard from the Retail app recently" — never proof of data
  freshness. No fake last-sync inference.
- **Commands:** closed enum `sync_now.v1` only. States
  `pending → leased → accepted → running → completed/failed`, with missing
  intermediates allowed (network loss). Cloud leasing (`FOR UPDATE SKIP
  LOCKED`, oldest-first, `lease_generation+1`) protects poll concurrency.
  Retail durable inbox keyed by command ID protects execution dedupe.
  These are different layers: a late legitimate ack/result from the target
  device is accepted even after lease expiry (unlike 7B worker fencing).
- **Durable-before-execute:** Retail commits the inbox row before ack and
  before execution; terminal state commits locally before any Cloud report.
  Report retries never re-execute. Crash during `running` recovers to a
  retryable state and re-runs the idempotent sync cycle (at-least-once
  execution, idempotent effects — never claimed exactly-once).
- **Sync authority:** one canonical cycle. Automatic and remote paths share
  a process-local gate; remote waits for a running automatic cycle, then
  runs its own full cycle. No table-specific sync code in 7C.
- **Dashboard:** Sync Now persists a command (`Idempotency-Key` required,
  unique per device; one active command enforced by a partial unique
  index) and returns. It never waits, dials Retail, or runs sync.
  User labels: Queued / Delivering / Received by device / Syncing /
  Completed / Failed.

## Consequences

- One Cloud migration (00017), one Retail SQLite migration (000006).
- 7D owns alerts/self-healing; 7C provides primitives only. No retention
  purge; no multi-store generalization (Phase 8).
