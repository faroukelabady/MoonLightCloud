# Operations: Incidents, Alerts & Bounded Self-Healing (Phase 7D)

Durable operational incidents over frozen notification/report/device
state, with provider-neutral operator alerts and one narrow automatic
recovery. No generic automation: 7D detects, deduplicates, notifies,
and heals only reconnects.

## Enable the engine

```bash
OPERATIONS_ENABLED=true
OPERATIONS_AUTO_SYNC_ON_RECONNECT=false   # healing stays off unless explicit
OPERATIONS_SCAN_INTERVAL=60s              # 10s..30m
OPERATIONS_SCAN_BATCH_SIZE=100            # 1..1000
OPERATIONS_DEVICE_OFFLINE_AFTER=5m        # >= DEVICE_ONLINE_WINDOW, 30s..24h
OPERATIONS_SYNC_PENDING_STALE_AFTER=30m
OPERATIONS_SYNC_RUNNING_STALE_AFTER=30m
OPERATIONS_REPORT_STALE_AFTER=60m
OPERATIONS_NOTIFICATION_RETRY_STALE_AFTER=60m
```

Disabled (default): Cloud stays healthy with no detector, no recovery
worker, no provider traffic, and no Sync Now creation. The dashboard and
CLI read paths still work. Enabling needs no provider network: incidents
exist even with zero recipients (dashboard is the fallback surface).

Startup never contacts Meta, WhatsApp, WooCommerce, or Retail.

## Alert recipients

```bash
moonlight-cloud operations recipients add --label ops --provider whatsapp-main --recipient 201012345678 --locale ar
moonlight-cloud operations recipients list    # addresses masked
moonlight-cloud operations recipients disable --id <uuid>
```

Recipients are configuration, isolated from business-report recipients.
Full addresses live only in the database, alert snapshots, and provider
requests — never in logs, metrics, incident APIs, or CLI output. Locales
are `ar`/`en`; labels reject control characters.

Each recipient needs a Phase 7A template mapping for
`operational_alert_open_v1` / `operational_alert_resolved_v1` with the
single `alert_body` parameter (existing `template-map` CLI). Missing
mappings block that recipient's deliveries (visible in the incident)
without failing the incident or touching Phase 7A.

## Incident meanings

- `DEVICE_OFFLINE` (warning, stateful): active device unseen past
  `OPERATIONS_DEVICE_OFFLINE_AFTER`. Never opens for never-seen devices.
  Resolves on reconnect or revocation (`DEVICE_NO_LONGER_ACTIVE`).
- `DEVICE_SYNC_FAILED` (urgent, event): one per failed sync command.
  Manual resolve only; never auto-retries.
- `DEVICE_SYNC_STALE` (warning, stateful): non-terminal command older
  than its threshold (pending vs accepted/running thresholds separate;
  single lease expiries never alert alone). Resolves at terminal state.
- `BUSINESS_REPORT_BLOCKED` (warning, event): one per blocked run.
  Alert only — never resends or recreates the report.
- `BUSINESS_REPORT_STALE` (warning, stateful): old pending/retry run.
- `NOTIFICATION_BLOCKED` (warning, event) / `NOTIFICATION_AMBIGUOUS`
  (urgent, event): one per business notification. Ambiguous means the
  delivery outcome is uncertain and needs manual investigation — 7D
  never resends. Alert-owned notifications are excluded (no storms).
- `NOTIFICATION_RETRY_STALE` (warning, stateful): old retryable
  notification; resolves when it leaves retry state.

## Acknowledge vs resolve

`Acknowledge` only clears the unacknowledged flag — it never claims the
device is back, the sync succeeded, or ambiguity disappeared. `Resolve`
closes event incidents; stateful incidents resolve automatically when
their condition clears, and manual resolve while active returns 409
`CONDITION_STILL_ACTIVE`. The guard runs in one transaction that locks
the incident row, then the predicate subject row (presence, command,
run, or notification), then applies a predicate-guarded UPDATE:
concurrent predicate writers block on that lock until commit, so a
condition turning active mid-flight is always observed rather than
resolved. The guard stays enforced even with the engine disabled (a
missing checker fails closed).

```bash
moonlight-cloud operations incidents list --state open
moonlight-cloud operations incidents status --id <uuid>
```

## Atomic intents and crash repair

Each incident transition commits together with its durable event intent
in one transaction: opening writes the incident plus its per-recipient
opened deliveries; resolution writes the resolved state plus its
resolved deliveries plus the reconnect recovery action. Concurrent
scanners converge on database uniqueness — exactly one delivery per
(incident, event, recipient) — and only the committing scanner proceeds.
Two boolean flags per incident (`open_intent_materialized`,
`resolved_intent_materialized`) distinguish an intentionally empty
snapshot (zero recipients) from a missing intent. Rows predating atomic
writes keep `FALSE` flags and are never auto-fabricated: an explicit
operator reconciliation (`operations incidents reconcile --id <uuid>`)
declares their current durable state complete, setting the applicable
flag with zero new deliveries, zero sends, and zero healing, preserving
existing snapshots byte-for-byte.

Historical note: detector builds predating this rule used to fill
missing intents automatically from then-current configuration. Any
deliveries or recovery rows created that way remain valid durable
state — adopted as-is, never deleted or rewritten. Only rows that are
still unmaterialized need explicit reconciliation; reconcile never
touches already-materialized snapshots.

## Reconnect Sync Now behavior

With `OPERATIONS_AUTO_SYNC_ON_RECONNECT=true`, a resolved offline
incident arms exactly one `DEVICE_RECONNECT_SYNC` action (deterministic
key `ops-reconnect-sync:<incident>`). The worker queues one canonical
`sync_now.v1` through Phase 7C: an existing active command satisfies the
action instead (manual/auto races converge, never duplicate). First-ever
contacts (`NEVER_SEEN`) never heal. If the auto command later fails, a
normal `DEVICE_SYNC_FAILED` incident opens and the chain stops — no
recursion, no repeated commands. Revocation before recovery runs blocks
the action.

## Troubleshooting alert delivery

Incident detail shows each delivery: `pending` (retrying), `sent`
(notification durably enqueued — provider lifecycle stays in 7A), or
`blocked` with a bounded code (`OPS_ALERT_NO_MAPPING`,
`OPS_ALERT_BODY_TOO_LARGE`, `OPS_ALERT_ENQUEUE_FAILED`). Crash between
enqueue and bookkeeping adopts the same notification on retry (one row,
one send).

## Troubleshooting recovery action

Detail shows `pending` (with attempt/backoff), `completed`
(`sync_command_created` with the command ID, or
`satisfied_by_existing_command`), or `blocked` (revoked device or
exhausted transient retries — capped at 10, then operator decides).

## Backup requirements

`operational_alert_recipients`, `operational_incidents`,
`operational_alert_deliveries`, and `operational_recovery_actions` are
durable operational audit/history: back them up. No rebuild deletes
them; no retention purge exists. Subjects and notifications are logical
references — history survives revocation and projection rebuilds.
