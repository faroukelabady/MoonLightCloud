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

Returns a `retry`/`blocked` event to `pending` (due now). The projector
picks it up on its next scan. The source event is untouched.

## Statuses

- `pending` — claimed or awaiting first attempt.
- `retry` — transient failure (contention, serialization); durable
  `next_attempt_at` with 5s × 2^attempt backoff capped at 1h.
- `blocked` — deterministic integrity failure (`SALE_ID_CONFLICT`,
  `VALIDATION_FAILED`); never retried automatically, never deleted.
- `processed` — complete projection committed.

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

## Rebuilds

Projection tables are derived and replayable from `sync_events`
(same event → identical rows via deterministic IDs + `ON CONFLICT DO
NOTHING`). No production rebuild command in Phase 2B; the property is
tested and available for a future controlled procedure.
