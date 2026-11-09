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

## Dashboard Settings

Report configuration is divided into separate panels:

1. **Configure Telegram / Configure WhatsApp** opens a provider template dialog
   with example report content and the relevant address-format notes. WhatsApp
   uses the exact approved Meta template name/language and one BODY variable.
2. **Recipients** lists masked addresses. Add, edit, enable/disable, or delete a
   recipient. The editor retrieves its full address only when opened. Provider
   identity is fixed during edits. WhatsApp addresses must start with the country
   code, without a leading local zero (`01012345678` → `201012345678` in Egypt).
   Telegram private recipients use numeric chat IDs, after starting the bot.
3. **Create / Edit schedule** sets name, cadence, Cairo time, first cycle date,
   and recipients. Edits use a revision check. Changing time/cadence selects the
   next future slot; other edits preserve the locked cursor.
4. **Configured schedules** supports edit, delete, pause/resume and send now.
5. **Recent sends** has ten-row pages, Previous/Next, and Refresh. Names stay
   readable after schedules are renamed or deleted.

Delete removes a configuration from future use and retains past sends. Reports
already queued keep their destination, language, period, name and report type,
and can still be sent. If deleting a recipient leaves a schedule without a
recipient, edit that schedule before its next run. Deleting configuration does
not cancel queued notifications or erase notification history.

The run label **Queued for delivery** describes preparation only. Check actual
notification dispatch/delivery status or the recipient's messaging app for
receipt. Example previews contain sample amounts, not live business figures.

The dashboard Settings page (all-Stores owners and admins with
`settings.manage`) manages everything below without the CLI: per-provider
report templates (WhatsApp approved template name and language; Telegram
uses the built-in `telegram_text_v1` text), recipients (shown masked),
schedules, enabling and disabling, and "send now" (a manual run keyed by a
client request key, so a double click sends once). Provider credentials
stay in the server environment; the page lists only providers enabled
there. Runs are delivered only while `BUSINESS_REPORTS_ENABLED=true`; the
page warns when the scheduler is off.

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
`anchor + 10k` calendar days. The configuration editor can change future cadence
in place. Runs keep immutable kind/name snapshots, periods and recipient
snapshots, so previously queued work retains its meaning (ADR-0059).

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
7A — not delivered or read. Delivery callbacks (`SENT`/`delivered`/
`READ`/`failed`) never rewrite report runs. A provider `failed`
later is a 7A delivery outcome, not a reason to resend the report.

## Delivery-write fencing

Every delivery mutation (snapshot persistence, terminal transitions)
runs inside a short transaction that locks the parent run row first
and validates owner, generation, status, and live lease expiry after
acquiring the lock. A worker whose lease expired — even while waiting
on a row lock — applies zero rows; the current lease owner converges
instead. Stale completions are safe no-ops, never errors.

## Backup requirements

`notification_recipients` (purpose `business_report`), `business_report_schedules`,
`business_report_schedule_recipients`, `business_report_runs`, and
`business_report_deliveries` are durable backup-required business
state. No rebuild path deletes them; no retention purge exists.

### Template settings and send history

Settings keeps the WhatsApp/Telegram template actions beside Add recipient.
Each provider dialog lists saved report/language mappings; Edit selects the
existing mapping rather than creating a new identity. The spaced example is
a layout preview. WhatsApp still sends each approved-template text variable
on one line; put permanent line breaks in the approved Meta template itself.
Telegram retains the report body's line breaks.

Recent sends uses keyset pagination and shows how many older pages remain.
The server counts older rows in the same query as the page; newer runs do not
inflate the remaining count while navigating older history. Refresh starts
again at the newest page. Completion here means queued for delivery, not a
provider delivery receipt.

### Actual message outcomes

Recent sends now reads the current notification records for each report's
recipients in one batched query. Report-run completion remains notification
handoff only; it is never rewritten to pretend delivery succeeded. The UI
shows queued, sending, retry scheduled, accepted/sent, delivered/read, failed,
partial failure or uncertain outcomes separately. WhatsApp delivery/read
states require its existing webhook. Telegram's sendMessage response proves
provider acceptance, not that a person read it. An uncertain outcome is not
safe to resend automatically.

The current history page refreshes every five seconds while visible, retaining
its keyset cursor. Refreshing status never sends/retries a message. Requests
are aborted and polling stops when the component closes. Failed refreshes
show a last-known-results notice. Full recipient addresses, report bodies,
provider message IDs and credentials are excluded from the status response.
