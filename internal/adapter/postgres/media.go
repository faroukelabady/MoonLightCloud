package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MediaBlobs records ADR-0056 image files in media_blobs. The bytes live in
// object storage (ADR-0057); a row means the file is stored there.
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
		Width: int32(blob.Width), Height: int32(blob.Height), UploadedByDevice: device,
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

// BlobInfo reads one stored file's record (no bytes).
func (d MediaBlobs) BlobInfo(ctx context.Context, sha256 string) (media.Blob, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	row, err := sqlcgen.New(d.pool).MediaBlobInfo(ctx, sha256)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return media.Blob{}, apperr.New(apperr.NotFound, "media file not stored")
		}
		return media.Blob{}, reportErr("media read", err)
	}
	return media.Blob{SHA256: row.Sha256, ContentType: row.ContentType, ByteSize: int(row.ByteSize),
		Width: int(row.Width), Height: int(row.Height)}, nil
}

var _ media.Repository = MediaBlobs{}
