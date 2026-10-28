package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MediaBlobs stores ADR-0056 image bytes in media_blobs.
type MediaBlobs struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

// NewMediaBlobs builds the blob store.
func NewMediaBlobs(pool *pgxpool.Pool, timeout time.Duration) MediaBlobs {
	return MediaBlobs{pool: pool, timeout: timeout}
}

// StoreBlob inserts the file unless the hash is already stored. A new file
// re-evaluates, in the same transaction, every Product whose gallery names
// it, so online channels publish images whose bytes arrived late.
func (d MediaBlobs) StoreBlob(ctx context.Context, blob media.Blob, deviceID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	device, err := parseUUID(deviceID)
	if err != nil {
		return false, apperr.New(apperr.Unauthorized, "missing device credential")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return false, reportErr("media store", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	rows, err := q.InsertMediaBlob(ctx, sqlcgen.InsertMediaBlobParams{
		Sha256: blob.SHA256, ContentType: blob.ContentType, ByteSize: int32(blob.ByteSize),
		Width: int32(blob.Width), Height: int32(blob.Height), Content: blob.Content, UploadedByDevice: device,
	})
	if err != nil {
		return false, reportErr("media store", err)
	}
	if rows == 1 {
		if _, err := q.EnqueueMediaBlobReevaluations(ctx, blob.SHA256); err != nil {
			return false, reportErr("media reevaluation", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, reportErr("media store", err)
	}
	return rows == 1, nil
}

// MediaContent reads one stored file for an online channel upload. Only
// Cloud reads blobs; they are never served to devices or shoppers.
func (d MediaBlobs) MediaContent(ctx context.Context, sha256 string) (commerce.MediaContent, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	row, err := sqlcgen.New(d.pool).MediaBlobContent(ctx, sha256)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.MediaContent{}, apperr.New(apperr.NotFound, "media file not stored")
		}
		return commerce.MediaContent{}, reportErr("media read", err)
	}
	return commerce.MediaContent{SHA256: row.Sha256, ContentType: row.ContentType, Bytes: row.Content}, nil
}

var (
	_ media.Repository            = MediaBlobs{}
	_ commerce.MediaContentReader = MediaBlobs{}
)
