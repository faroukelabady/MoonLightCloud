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
// the existing row is compared. Identical retry → already_accepted;
// same ID with different payload or different device → deterministic
// EVENT_ID_REUSE conflict. ACK is returned only after commit.
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
				if uuidString(existing.DeviceID) != deviceID || !bytes.Equal(existing.PayloadHash, ev.Hash) {
					return nil, apperr.New(apperr.Conflict,
						fmt.Sprintf("EVENT_ID_REUSE: event %s already accepted with different device or payload", ev.EventID))
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
