# Sync Protocol Architecture (future — documented, not implemented)

```
MoonLightRetail (SQLite, offline-first)
      │ versioned HTTPS, at-least-once delivery
      ▼
MoonLightCloud (/api/v1, PostgreSQL)
```

## Principles

1. **Offline-first desktop.** The register works without internet. Cloud is
   a projection, never the register's dependency.
2. **Transactional local outbox.** Desktop writes business rows + outbox rows
   in one SQLite transaction. No outbox row, no send.
3. **Idempotent cloud ingestion.** Every event carries a stable `event_id`
   (UUIDv7) and `idempotency_key`. Retries are safe by construction.
4. **At-least-once delivery assumption.** Network fails; duplicates arrive.
5. **Server-side deduplication.** Cloud keeps consumed event IDs per device
   and acks duplicates without re-applying effects.
6. **Versioned event payloads.** `event_type` + `event_version`; unknown
   versions are rejected with a clear code, never silently applied.
7. **Backward compatibility.** Old desktops keep syncing across cloud
   deploys. Breaking changes need a new `/api/v2` or event version + ADR.

## Future request shape (directional)

- Auth: device credential (this phase) over HTTPS.
- Mutations carry `Idempotency-Key`; responses echo consumed `event_id`s.
- Replay protection beyond HTTPS + credentials + idempotency is deferred
  until threat analysis demonstrates need (ADR territory).

## Explicitly not in Phase 1A

No outbox, no ingestion endpoints, no projections (sales/returns/inventory),
no shared domain package — duplication across the contract is intended.
