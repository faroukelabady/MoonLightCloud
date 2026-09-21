# Sale Projection Operations

Derived state from the immutable `sync_events` inbox. Never delete or edit
inbox rows to fix projection; remediation is auditable and preserves both
source events.

## Status

```bash
moonlight-cloud projection status
```

Shows per-status counts for `sale_projection.v1`, oldest pending age, and
the latest error (code + bounded safe message). No dashboard required.

## Retry one event

```bash
moonlight-cloud projection retry <event-id>
```

Returns a `retry`/`blocked` event to `pending` with `next_attempt_at = NULL`
(immediately discoverable on the next scan). The source event is untouched.
The CLI writes durable state directly; the in-process projector picks it up
on its next wake/scan (no cross-process wake channel).

## Statuses

- `pending` — claimed or awaiting first attempt. Discoverable.
- `retry` — transient failure; durable `attempt_count` + `next_attempt_at`
  with `5s × 2^attempt` backoff capped at 1h (overflow-safe, saturating).
  Discoverable only when `next_attempt_at <= now` (NULL counts as due).
  Never hot-looped: the projection transaction rolls back, then retry state
  commits in a separate durable transaction, so restarts preserve the
  schedule and attempts never spin before due time.
- `blocked` — deterministic integrity failure (`SALE_ID_CONFLICT`,
  `VALIDATION_FAILED`); never retried automatically, never deleted, never
  returned by discovery scans.
- `processed` — complete projection committed. Never rediscovered.

## Ownership (CRIT-01)

Exactly one event owns a `sale_id`, decided atomically by
`INSERT ... ON CONFLICT DO NOTHING RETURNING` inside the projection
transaction — never SELECT-then-INSERT. The loser inserts zero children.
Same `source_event_id` replays idempotently; any other event with the same
sale_id becomes `SALE_ID_CONFLICT`.

## Logical sale conflict

Two different events carrying the same `sale_id`: both stay durably
accepted (ACK was correct), the first projected wins, the loser is
`blocked` with `SALE_ID_CONFLICT`. Investigate the desktop outbox
(double submission with regenerated IDs indicates a client bug); resolve
on the desktop side with a correction event in a future version. Never
overwrite the projected sale, never delete either event.

## Crash recovery

- Crash before projection commit → transaction rolled back → event stays
  discoverable → next scan completes it. No desktop resend needed.
- Crash after commit → row already `processed` → recognized, no duplicates.
- Restart always rescans durable state; no in-memory signal required.

## Blocked events and health

Blocked projections are operational conditions: `/health/live` and
`/health/ready` stay 200. Watch `projection status` instead.

## Authority model (rebuild safety)

Three tiers — never confuse them:

- Durable source history: `sync_events` (immutable, never deleted).
- Durable logical ownership: `sale_event_ownership` (one permanent winner
  per `sale_id`; never deleted on rebuild).
- Rebuildable state: `sales_projection`, lines, payments, classifications,
  and `sync_event_processing` rows (may be cleared/reset, then reprocessed).

A rebuild that deletes ownership can elect a different winner and rewrite
financial history. The procedure below never does.

## Ownership integrity query

```sql
-- Any row here is an integrity failure: projected source differs from the
-- durable winner. Investigate before serving reads from projections.
SELECT s.sale_id, s.source_event_id AS projected_source, o.winning_event_id
FROM sales_projection s
JOIN sale_event_ownership o USING (sale_id)
WHERE s.source_event_id <> o.winning_event_id;
```

Resetting a losing conflict event during rebuild deterministically returns
it to `SALE_ID_CONFLICT` from durable ownership; it never temporarily
becomes the winner. Ordinary `SALE_ID_CONFLICT` volume never affects
`/health/ready` (blocked projections are operational conditions); schema
absence/version mismatch still fails readiness per startup checks.

## Rebuilds

Projection tables are derived and replayable from `sync_events`
(same event → identical rows via deterministic IDs + `ON CONFLICT DO
NOTHING`).

### Development / no production data

Reset/rebuild of derived tables is acceptable: the inbox stays
authoritative and reprocessing reproduces identical rows (tested by
`TestProjectionRebuildFromInbox`).

### Pilot data may exist

1. Back up PostgreSQL (`docs/operations/backup.md`).
2. Identify conflicting/mixed projection candidates from before the
   atomic-ownership fix:
   ```sql
   SELECT sale_id, count(DISTINCT source_event_id) FROM sales_projection GROUP BY sale_id HAVING count(*) > 1;
   SELECT p.sale_id FROM sale_lines_projection l JOIN sales_projection p USING (sale_id)
     GROUP BY p.sale_id, p.source_event_id HAVING count(*) <> (
       SELECT count(*) FROM sale_lines_projection WHERE sale_id = p.sale_id);
   ```
   Simpler robust check: any `sale_id` whose lines/payments/classifications
   do not exactly match a re-projection of its `source_event_id` payload.
3. Clear derived tables only (never `sync_events`, never
   `sale_event_ownership`):
   ```sql
   DELETE FROM sales_projection; -- cascades to lines/payments/classifications
   UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
     attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
     WHERE processor='sale_projection.v1';
   ```
4. Reprocess from the durable inbox (restart projector / wait for scan).
5. Verify: `projection status` shows all `processed`, counts match inbox,
   and a row-level dump matches the pre-rebuild snapshot for unaffected
   sales. Never delete accepted source events. No automated rebuild CLI in
   this phase; the documented procedure plus the rebuild test suffice.
