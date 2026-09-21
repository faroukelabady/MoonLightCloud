# Sync Protocol Contract (Phase 1B transport + Phase 2B business events)

This is the contract MoonLightRetail's transactional outbox implements
against: generic envelope and ingestion semantics plus the first business
event `sale.finalized.v1` — validated before ACK, projected asynchronously
(see docs/operations/projection.md and ADR-0016).

## Authentication

All `/api/v1/sync/*` require device auth:

```
Authorization: Bearer <device-id>.<credential-id>.<secret-hex>
```

Events are attributed to the **authenticated** device server-side.
`device_id` may be echoed in an envelope but must equal the authenticated
device, else `403`. Impersonation is impossible through payload fields.

## Canonical endpoint

```
POST /api/v1/sync/batches
Content-Type: application/json
```

Batching is the only shape (efficient for queued outbox drains).

```json
{
  "batch_id": "optional-client-uuid-echoed-back",
  "events": [
    {
      "event_id": "uuid-generated-with-outbox-row-never-regenerated-on-retry",
      "event_type": "system.test.v1",
      "device_id": "optional-must-match-auth",
      "occurred_at": "2026-09-19T10:20:30.123456Z",
      "payload": {}
    }
  ]
}
```

Field rules:

- `event_id`: any valid UUID, UUIDv7 preferred (time-ordered, index-local;
  server accepts any well-formed UUID so future clients are never pointlessly
  rejected). Same logical event keeps its ID across retries — this is the
  foundation of idempotency.
- `event_type`: `name.version` (`sale.finalized.v1` shape), immutable once
  released. The suffix carries payload semantics; no separate global protocol
  integer (`/api/v1` = envelope semantics, suffix = payload semantics).
- `occurred_at`: RFC3339 business timestamp. Recorded, never authority for
  auth/dedup/ordering. Bounds: not before 2020-01-01, not more than 24h in
  the future, else `422`. Desktop clocks are untrusted.
- `received_at`: server clock at commit. Never substituted for `occurred_at`.
- `payload`: required JSON **object**, max 256 KiB. Future money uses integer
  minor units + currency code (`{"amount_minor":125000,"currency":"EGP"}`),
  never floats. Domain references are UUID strings. Never credentials,
  tokens, card data, or provider secrets in payloads.
- Unknown additive envelope fields are **tolerated** (forward compat);
  duplicate JSON keys are **rejected** as client errors. Field order is
  insignificant.

## Durable ingestion

Accepted events persist in `sync_events` (`event_id` PK globally unique,
`device_id`, `credential_id` audit, `event_type`, `occurred_at`,
`received_at`, `payload` JSONB, `payload_hash`). Events are immutable and
never deleted in Phase 1B; corrections are new events
(`sale.corrected.v1`, never rewrites).

> **ACK means MoonLightCloud durably committed the event to PostgreSQL and
> future retries with the same event identity will be idempotently
> recognized.** ACK does NOT mean parsed, queued, or projected. No ACK is
> returned before commit; projection completion is never required for ACK.

## Idempotency

- First send → `accepted`. Identical retry (same ID, same canonical
  payload) → `already_accepted`. Both let the desktop clear the outbox item.
- Same ID + different payload (or different device) → `409 EVENT_ID_REUSE`.
  Payload sameness compares SHA-256 over canonical JSON (sorted keys,
  normalized numbers), so field order/whitespace never cause false mismatch.
- Idempotency is tied to **event identity + device**, not credential:
  rotating credentials then retrying still deduplicates.

## Batch semantics

- Limits: max 100 events, max 256 KiB per payload, max 8 MiB body.
- **All-or-nothing**: one malformed event → nothing commits; retry the
  whole batch. Success response is `200` with per-event statuses
  (`accepted` / `already_accepted` only).
- Duplicate IDs inside one batch → `400` (client construction error).
- `batch_id` is an optional client UUID echoed back for support/debugging;
  it never replaces event-level idempotency. No `sync_batches` table:
  batches are a transport unit, not durable identity.

## Status / retry contract

| Status | Meaning | Retry? |
|---|---|---|
| 200 | committed (per-event accepted/already_accepted) | no |
| 400 malformed envelope | client bug | no — fix |
| 401 | bad/missing credential | no — re-provision/rotate |
| 403 | device mismatch / forbidden | no |
| 409 EVENT_ID_REUSE | same ID, different content | no — new event_id |
| 413 too large | over limits | no — shrink |
| 415 wrong content type | must be application/json | no |
| 422 unsupported/invalid semantics | unknown type, absurd timestamp | no |
| 429 / 5xx | throttling / transient | **yes**, exponential backoff |
| 503 + `Retry-After` where sent | DB/service unavailable | yes |

500 is conservatively retryable only for idempotent event sends (same IDs);
all-or-nothing batches make replays safe.

## Ordering

HTTP order is not business order. Events arrive late, repeated, out of
order. `event_id` = identity, `occurred_at` = business time, `received_at`
= receipt time; none alone is authoritative sequence. No universal sequence
counters until a real business event needs them.

## Compatibility

Cloud supports older event versions for a defined window; desktop and cloud
releases never need to match. `GET /api/v1/sync/capabilities`
(authenticated) reports `api_version`, `supported_events`, limits, and
`server_time` (drift diagnosis only, never auth). No infrastructure
internals exposed.

## Security notes

- Replay of a captured event is harmless: identical → already accepted,
  altered → conflict. No custom signing/nonces (reviewed, deferred).
- Revoked devices fail immediately; committed history is kept.
- Logs carry counts/statuses/IDs, never payloads. Request bodies bounded.

## Sequence diagrams

Normal:

```
Desktop ── event E ──▶ Cloud ── commit ──▶ Postgres ── ACK ──▶ Desktop
```

Lost ACK:

```
Desktop ── E ──▶ Cloud ── commit E ──▶ Postgres ── ACK lost ✕
Desktop ── retry E ──▶ Cloud detects duplicate ── already_accepted ──▶ Desktop
```

Payload mismatch:

```
same event ID + different contents ──▶ 409 EVENT_ID_REUSE
```

## Examples (`system.test.v1` — diagnostic, safe to send)

```bash
AUTH="Bearer <device-id>.<credential-id>.<secret>"
curl -X POST localhost:8080/api/v1/sync/batches \
  -H "Authorization: $AUTH" -H 'Content-Type: application/json' -d '{
  "events": [{
    "event_id": "22222222-2222-7222-8222-222222222222",
    "event_type": "system.test.v1",
    "occurred_at": "2026-09-19T10:20:30Z",
    "payload": {"ping": 1}
  }]}'
# → {"events":[{"event_id":"...","status":"accepted"}]}
```
