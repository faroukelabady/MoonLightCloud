# ADR-0035 — Scheduled Business Reports

## Status

Accepted (Phase 7B implementation).

## Context

Operators need daily and 10-day business summaries delivered over the
Phase 7A notification infrastructure without giving business phases
any knowledge of WhatsApp, Meta templates, or provider transport.

## Decision

- **Existing reporting domain remains calculation authority.**
  Scheduled runs resolve civil periods and call the frozen
  `report.Service.Summary` (custom periods) directly. No financial
  SQL, refund arithmetic, or money handling is duplicated; EGP/USD
  stay separate with int64 minor units end to end.
- **Daily/10-day periods** are previous complete Cairo calendar days:
  slot D reports `[D-1, D)` / `[D-10, D)`. Recurrence uses local
  calendar arithmetic (`AddDays`), never 24h/240h elapsed durations,
  so DST transitions stay exact. TEN_DAY slots chain from an explicit
  anchor (`anchor + 10k`) at the configured wall-clock time.
- **Durable schedule slots.** Each schedule carries `next_run_*` as
  its materialization cursor. The planner claims due schedules
  (`FOR UPDATE SKIP LOCKED`), then one transaction locks the row,
  verifies enabled/due, computes the exact slot and period, inserts
  the run plus recipient-snapshot deliveries, and advances the
  cursor. Run creation and cursor advancement commit atomically, so
  crashes leave neither duplicates nor gaps; multi-instance planners
  converge on one run per slot. Missed slots materialize oldest-first
  in bounded batches; intentional disable periods never backfill.
- **Multi-instance planning** needs no lease: the row lock inside the
  short planner transaction is sufficient. No schedule lock is held
  during report composition or notification enqueue.
- **Run lease fencing** (Phase 6C/7A pattern): every run claim
  increments `lease_generation`; every finish and snapshot persist
  requires owner + generation on a non-terminal run. Stale workers
  update zero rows.
- **Report snapshot immutability.** Delivery bodies snapshot once
  (first writer wins under the run lease) and are never recomputed:
  late syncs, returns, or repairs cannot silently alter a queued
  message. A corrected report is a NEW explicit run. Snapshots are
  per-locale delivery bodies plus a deterministic fingerprint over
  kind, period, locale, and body.
- **Recipient snapshots.** Deliveries freeze recipient ID, provider
  key, address, and locale at materialization; later edits/disables
  affect future runs only.
- **Phase 7A enqueue idempotency.** Deliveries call only
  `EnqueueTemplate` with `business-report:<run>:<delivery>` keys
  (UUIDs only, no PII). Crash-after-enqueue converges on the same
  notification via 7A idempotent replay — one row, one send.
  Conflicts block with a consistency code instead of minting new
  keys. Run completion means all notifications durably enqueued;
  provider delivery (`SENT`/`DELIVERED`/`READ`/`FAILED`) stays owned
  by Phase 7A and never rewrites 7B state.
- **7C/7D boundaries.** No device-connectivity checks, no sync
  requests, no freshness claims beyond the canonical report's own
  projection metadata, no alert rules. Report freshness lines use
  only the frozen report service's projection timestamps.

## R1 remediation (run consistency, lease fencing, schedule lifecycle)

The 7B freeze review found six material inconsistencies, closed
without touching reporting math, provider code, or the schema:

1. **Run-wide snapshot barrier.** Bodies were composed lazily per
   delivery, so late data could reach unsent recipients. The runner
   now snapshots every pending delivery from ONE canonical Summary
   result in a single lease-fenced transaction before the first
   enqueue; partial pre-existing snapshot sets block safely instead
   of mixing bases.
2. **Delivery lease fencing.** Every delivery mutation (snapshot,
   enqueued, blocked) requires the current run lease
   (owner+generation, unexpired) on a non-terminal run. Stale owners
   affect zero rows; run verdicts re-read durable rows.
3. **Arithmetic anchor resolution.** TEN_DAY slots resolve with O(1)
   civil-date math instead of a bounded 400-slot scan, so old
   anchors (e.g. 2000-01-01) work.
4. **Idempotent enable.** Enabling an enabled schedule is a no-op
   preserving cursor and backlog; only disabled→enabled skips ahead
   (exactly-once under concurrency).
5. **Manual semantic idempotency.** Replays compare kind, period,
   recipient IDs, addresses, providers, locales, and templates
   order-independently; labels never conflict.
6. **Atomic creation.** Schedule plus links commit in one
   recipient-locking transaction; failures leave nothing behind and
   never consume the unique name.
7. **UUIDv7 + batch parsing.** New 7B IDs use the repository UUIDv7
   generator; batch size parses wide before narrowing.

## Consequences

- 7B operator surface is CLI only (recipients, schedules, run-now,
  runs); no dashboard UI, no public HTTP API, no bulk tooling.
- All 7B tables are durable backup-required business state with no
  destructive FKs into rebuildable projections.
- Manual `run-now` creates distinct idempotent runs that never move
  `schedule.next_run`.
