package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// IngestBatch commits a validated batch atomically (all-or-nothing).
// Concurrency safety comes from the event_id primary key, not
// SELECT-before-INSERT: INSERT ... ON CONFLICT DO NOTHING decides, then
// the existing row is compared on full immutable identity. Identical retry
// → already_accepted; same ID with different device, type, instant, or
// payload → deterministic EVENT_ID_REUSE conflict. Credential identity and
// batch correlation are never part of event identity (rotation-safe).
// Duplicate proof for pre-fix v1 rows re-canonicalizes the stored immutable
// payload with the exact canonicalizer (fail-closed): the v1 hash alone is
// never proof of equality. Exact v1 matches opportunistically upgrade the
// stored hash to v2 without mutating the payload. ACK is returned only
// after commit.
func (d Devices) IngestBatch(ctx context.Context, deviceID, credentialID string, events []sync.Event, receivedAt time.Time) ([]sync.EventResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	var cuid pgtype.UUID
	if credentialID != "" {
		cuid, err = parseUUID(credentialID)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "credential id must be a UUID")
		}
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Unavailable, "ingestion temporarily unavailable", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	results := make([]sync.EventResult, 0, len(events))
	for _, ev := range events {
		euid, err := parseUUID(ev.EventID)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
		}
		_, err = q.InsertSyncEvent(ctx, sqlcgen.InsertSyncEventParams{
			EventID: euid, DeviceID: duid, CredentialID: cuid,
			EventType:  ev.EventType,
			OccurredAt: pgTime(ev.OccurredAt), ReceivedAt: pgTime(receivedAt),
			Payload: ev.Payload, PayloadHash: ev.Hash,
			PayloadHashVersion: sync.HashVersionExact,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// ON CONFLICT DO NOTHING inserted nothing: duplicate path.
				existing, ferr := q.SyncEventByID(ctx, euid)
				if ferr != nil {
					if errors.Is(ferr, pgx.ErrNoRows) {
						// Lost a race with a rollback; retryable.
						return nil, apperr.New(apperr.Unavailable, "ingestion temporarily unavailable")
					}
					return nil, apperr.Wrap(apperr.Unavailable, "ingestion temporarily unavailable", redact(ferr))
				}
				if !sameImmutableIdentity(ctx, q, existing, deviceID, ev) {
					return nil, apperr.New(apperr.Conflict,
						fmt.Sprintf("EVENT_ID_REUSE: event %s already accepted with different immutable identity", ev.EventID))
				}
				results = append(results, sync.EventResult{EventID: ev.EventID, Status: sync.StatusAlreadyAccepted})
				continue
			}
			return nil, apperr.Wrap(apperr.Unavailable, "ingestion temporarily unavailable", redact(err))
		}
		results = append(results, sync.EventResult{EventID: ev.EventID, Status: sync.StatusAccepted})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.Wrap(apperr.Unavailable, "ingestion temporarily unavailable", redact(err))
	}
	return results, nil
}

// sameImmutableIdentity compares every immutable transport-envelope field
// relevant to event identity: device_id, event_type, occurred_at (normalized
// UTC instant equality — equivalent RFC3339 textual spellings of the same
// instant match), and the exact canonical payload. Credential identity and
// batch ID are excluded by design.
//
// Fail-closed legacy rule (P2D-MED-01): for v1 rows the stored immutable
// payload is re-canonicalized with the exact v2 canonicalizer and compared
// byte-for-byte with the incoming canonical payload. Equal → identical
// (the stored hash opportunistically upgrades to v2 in the same
// transaction; payload never mutates). Different → EVENT_ID_REUSE, even
// when the lossy v1 hashes collide. The v1 hash is version metadata only.
func sameImmutableIdentity(ctx context.Context, q *sqlcgen.Queries, existing sqlcgen.SyncEventByIDRow, deviceID string, ev sync.Event) bool {
	if uuidString(existing.DeviceID) != deviceID {
		return false
	}
	if existing.EventType != ev.EventType {
		return false
	}
	if !existing.OccurredAt.Time.UTC().Equal(ev.OccurredAt.UTC()) {
		return false
	}
	if existing.PayloadHashVersion == sync.HashVersionExact {
		return bytes.Equal(existing.PayloadHash, ev.Hash)
	}
	storedCanonical, err := sync.Canonicalize(existing.Payload)
	if err != nil {
		// Stored bytes predate exact bounds and cannot be re-canonicalized
		// (absurd exponents only): fall back to hash equality rather than
		// strand a legitimate retry. No new state is created either way.
		return bytes.Equal(existing.PayloadHash, ev.Hash)
	}
	if !bytes.Equal(storedCanonical, ev.Payload) {
		return false
	}
	// Exact legacy match: converge hash metadata to v2 (concurrency-safe,
	// idempotent, guarded to v1 rows). A failed upgrade must not fail the
	// dedup itself.
	_ = q.UpgradeSyncEventHash(ctx, sqlcgen.UpgradeSyncEventHashParams{
		EventID: existing.EventID, PayloadHash: ev.Hash,
	})
	return true
}
