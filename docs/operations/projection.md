# Sale & Return Projection Operations

Derived state from the immutable `sync_events` inbox. Never delete or edit
inbox rows to fix projection; remediation is auditable and preserves both
source events.

## Status

```bash
moonlight-cloud projection status
```

Shows per-status counts for `sale_projection.v1` and
`return_refund_projection.v1`, oldest pending age, and the latest error
(code + bounded safe message) per processor. No dashboard required.

## Retry one event

```bash
moonlight-cloud projection retry <event-id> [processor]
```

Processor defaults to `sale_projection.v1`; pass
`return_refund_projection.v1` for returns.

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
- `blocked` — deterministic failure (`SALE_ID_CONFLICT`,
  `VALIDATION_FAILED`, `OWNERSHIP_INTEGRITY`, plus return codes
  `RETURN_REFUND_ID_CONFLICT`, `RETURN_LINE_UNKNOWN`,
  `RETURN_CURRENCY_MISMATCH`, `RETURN_FX_MISMATCH`,
  `CUMULATIVE_OVER_RETURN`, `CUMULATIVE_REFUND_EXCEEDED`); never retried
  automatically, never deleted, never returned by discovery scans.
- `retry` with code `SALE_DEPENDENCY_WAIT` means the return is valid but
  its original sale projection is not yet present (out-of-order arrival):
  it projects automatically once the sale appears. This is a dependency
  wait, not a failure — distinct from terminal `blocked`.
- `processed` — complete projection committed. Never rediscovered.

## Return projection

`return_refund_projection.v1` reuses the same mechanism with an independent
processor name. Accepted return events with no processing row are
discoverable (Phase 4A history backfills with no resend). Returns serialize
per sale on a parent-row lock so cumulative guards
(Σ returned qty ≤ sold qty; Σ refunds ≤ sale total) cannot write-skew.
Return line cost is the extended historical cost — never re-multiplied.
Exactly one event owns a `return_refund_id` in `return_refund_ownership`
(many returns per sale stay valid); rebuilds clear return projections but
never ownership. Return reporting date is the return `occurred_at`.

## Ownership

Exactly one event owns a `sale_id`, decided by a bare
`INSERT ... ON CONFLICT DO NOTHING RETURNING` that absorbs either uniqueness
race (same-sale rivals on `sale_id`, same-event replays on
`winning_event_id`) — never SELECT-then-INSERT. The loser inserts zero
children. Same `winning_event_id` replays idempotently; any other event
with the same sale_id becomes `SALE_ID_CONFLICT`; a winner bound to a
different sale is a deterministic `OWNERSHIP_INTEGRITY` failure.

## Transaction model

Two separate transactions — ownership is NOT established inside the
projection transaction:

```text
Transaction A:
    durable Sale ownership arbitration
    COMMIT
        ↓
Transaction B:
    derived Sale projection
    processing-state transition
    COMMIT / ROLLBACK
```

If projection transaction B fails and rolls back, ownership from A remains:
that is intentional. `sale_event_ownership` is durable correctness history,
not disposable projection state. Projection rollback does not roll back
Sale ownership. Projection rebuild must retain `sale_event_ownership`.
Operators must never delete ownership rows during ordinary rebuild.

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

- Durable source history: `sync_events` (immutable accepted event history;
  never deleted).
- Durable logical ownership: `sale_event_ownership` (one permanent winner
  per `sale_id`) and `return_refund_ownership` (one permanent winner per
  `return_refund_id`; multiple distinct returns per sale stay valid).
  Both are durable authoritative arbitration history — never deleted on
  rebuild.
- Rebuildable state: `sales_projection` (+ lines, + payments, +
  classifications), `return_refund_projection` (+ lines, + payments), and
  `sync_event_processing` rows for BOTH processors (may be cleared/reset,
  then reprocessed).

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
SELECT r.return_refund_id, r.source_event_id AS projected_source, o.winning_event_id
FROM return_refund_projection r
JOIN return_refund_ownership o USING (return_refund_id)
WHERE r.source_event_id <> o.winning_event_id;
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
3. Clear derived tables only (never `sync_events`, never either ownership
   table). Deleting `sales_projection` cascades — by the FK graph — into
   sale lines, payments, classifications AND `return_refund_projection`
   (+ return lines, + return payments), because return lines reference
   their sale lines. Either delete sale projections first (returns follow
   via cascade) or clear both explicitly:
   ```sql
   DELETE FROM return_refund_projection; -- return lines + payments
   DELETE FROM sales_projection; -- sale lines/payments/classifications
   ```
4. Reset BOTH processing identities (resetting only `sale_projection.v1`
   leaves Return events marked `processed` while their projection rows are
   gone — reports would silently lose refunds):
   ```sql
   UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
     attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
     WHERE processor IN ('sale_projection.v1', 'return_refund_projection.v1');
   ```
5. Replay the Sale processor first (restart projector / wait for scan),
   then allow the dependent Return processor to replay. Returns whose
   sale is not yet projected wait with `SALE_DEPENDENCY_WAIT` and
   converge automatically — do not force-block them; wait for
   retry/dependency convergence.
6. Verify: `projection status` shows both processors with zero backlog
   (processed + blocked == accepted per event type), `blocked`/`retry`
   counts match pre-rebuild expectations (rivals stay blocked with the
   same winner; dependency waits resolve), the ownership integrity
   queries above return zero rows, and a row-level dump matches the
   pre-rebuild snapshot. Financial check: summary gross/refund/net and
   gross/returned/net cost totals must be semantically identical before
   and after. Never delete accepted source events. No automated rebuild
   CLI in this phase; the documented procedure plus the rebuild tests
   (`TestReturnRebuildStable`, `TestReturnRivalRebuildStable`,
   `TestReturnOutOfOrderRebuildStable`) suffice.
