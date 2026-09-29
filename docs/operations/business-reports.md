# Scheduled Business Reports (Phase 7B)

Daily and 10-day aggregate management summaries composed from the
canonical Cloud reporting domain and delivered through Phase 7A
notifications. Cairo-calendar aware; no automatic triggers beyond
configured schedules.

## Enable the scheduler

```bash
BUSINESS_REPORTS_ENABLED=true
BUSINESS_REPORTS_POLL_INTERVAL=60s   # 5s..1h
BUSINESS_REPORTS_BATCH_SIZE=25       # 1..100
BUSINESS_REPORTS_LEASE_DURATION=5m   # 1m..1h
```

Disabled (default): no planner, no runner, CLI configuration still
works, Cloud stays healthy. Enabling needs no provider network:
schedules and recipients are local Cloud state.

## Prerequisites per notification provider

For each recipient provider (today `whatsapp-main`), configure the
Phase 7A logical template mapping first:

```bash
moonlight-cloud notifications template-map set \
  --provider whatsapp-main \
  --template daily_business_report_v1 \
  --locale ar \
  --external-name moonlight_daily_report_ar \
  --language ar \
  --params report_body
```

Same for `ten_day_business_report_v1` (and `en` mappings for English
recipients). 7B only knows these logical keys.

## Create a recipient

```bash
moonlight-cloud business-reports recipients add \
  --label owner \
  --provider whatsapp-main \
  --recipient 201012345678 \
  --locale ar
```

Output shows a masked recipient (`...5678`). Recipients are delivery
configuration, not a CRM: label, provider, address, locale only.
Disable with `recipients disable --id`; materialized runs keep their
snapshots.

## Create schedules

```bash
moonlight-cloud business-reports schedules create \
  --name owner-daily \
  --kind daily \
  --time 21:00 \
  --recipient <recipient-uuid>

moonlight-cloud business-reports schedules create \
  --name owner-ten-day \
  --kind ten-day \
  --time 21:00 \
  --anchor-date 2026-10-01 \
  --recipient <recipient-uuid>
```

Timezone is always `Africa/Cairo`. DAILY slots report the previous
complete local day; TEN_DAY slots report the previous ten complete
local days from an explicit anchor (`anchor`, `anchor+10`, …) at the
configured wall-clock time. Creation positions the first future slot
and never backfills history.

## Anchor semantics

TEN_DAY business cycles are deterministic: slot dates are exactly
`anchor + 10k` calendar days. Changing cadence means disabling the
old schedule and creating a new one (no in-place edits, no
historical ambiguity).

## List schedules

```bash
moonlight-cloud business-reports schedules list
moonlight-cloud business-reports schedules disable --id <uuid>
moonlight-cloud business-reports schedules enable --id <uuid>
```

Disabling stops future materialization but keeps history, pending
runs, and notifications. Re-enabling starts from the first future
slot — the disabled interval is intentionally skipped. Enabling an
already-enabled schedule changes nothing: cursor and backlog are
preserved.

## Manual run-now

```bash
moonlight-cloud business-reports run-now \
  --schedule <uuid> \
  --idempotency-key manual-20260928-001
```

Covers the previous complete period(s) from the current Cairo date
(partial today excluded). The manual key makes the run idempotent;
changed semantics under the same key conflict. Manual runs never move
`schedule.next_run`. Use a NEW key to deliberately resend corrected
figures after late data.

## Inspect runs

```bash
moonlight-cloud business-reports runs list
moonlight-cloud business-reports runs status --id <run-uuid>
```

Status shows per-delivery outcomes (`enqueued N`, `blocked M`) with
masked recipients and machine error codes — never report bodies or
full addresses.

## Blocked run troubleshooting

- `REPORT_NO_RECIPIENTS`: link an enabled recipient (new schedule or
  re-enable), or accept the blocked run as history.
- `REPORT_NOTIFICATION_MAPPING_MISSING`: create the Phase 7A logical
  mapping for the run's template key/locale.
- `REPORT_NOTIFICATION_VALIDATION`: provider/config rejected the
  enqueue (check Phase 7A provider status).
- `REPORT_NOTIFICATION_IDEMPOTENCY_CONFLICT`: consistency condition —
  preserve data, diagnose manually, do not re-enqueue.
- `REPORT_BODY_TOO_LARGE`: v1 summary exceeded the 1024-byte
  parameter bound; blocked rather than truncated.

## Report period semantics

Slot on local date D (DAILY) covers `[D-1 00:00, D 00:00)` Cairo;
TEN_DAY slot covers `[D-10 00:00, D 00:00)` Cairo. Periods are local
calendar days (23/24/25 actual hours around DST transitions), never
24h/240h durations. The schedule clock sets delivery time, never the
period end. Bodies show gross/refunds/net/cost per currency bucket
plus transaction counts and a `Cloud data through:` projection stamp
from canonical report metadata (which proves nothing about Retail
sync — Phase 7C owns connectivity).

## Missed-slot catch-up

Downtime never loses due slots: after restart every missed enabled
slot materializes oldest-first in bounded batches across planner
ticks, without skipping or duplicating. Restarting after success
changes nothing: completed runs stay completed, notifications stay
singular.

## Notification relationship

Run `completed` means every delivery is durably enqueued in Phase
7A — not delivered or read. Delivery callbacks (`SENT`/`DELIVERED`/
`READ`/`FAILED`) never rewrite report runs. A provider `FAILED`
later is a 7A delivery outcome, not a reason to resend the report.

## Delivery-write fencing

Every delivery mutation (snapshot persistence, terminal transitions)
runs inside a short transaction that locks the parent run row first
and validates owner, generation, status, and live lease expiry after
acquiring the lock. A worker whose lease expired — even while waiting
on a row lock — applies zero rows; the current lease owner converges
instead. Stale completions are safe no-ops, never errors.

## Backup requirements

`business_report_recipients`, `business_report_schedules`,
`business_report_schedule_recipients`, `business_report_runs`, and
`business_report_deliveries` are durable backup-required business
state. No rebuild path deletes them; no retention purge exists.
