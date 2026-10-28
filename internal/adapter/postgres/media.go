package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
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

// StoreBlob inserts the file unless the hash is already stored.
func (d MediaBlobs) StoreBlob(ctx context.Context, blob media.Blob, deviceID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	device, err := parseUUID(deviceID)
	if err != nil {
		return false, apperr.New(apperr.Unauthorized, "missing device credential")
	}
	rows, err := sqlcgen.New(d.pool).InsertMediaBlob(ctx, sqlcgen.InsertMediaBlobParams{
		Sha256: blob.SHA256, ContentType: blob.ContentType, ByteSize: int32(blob.ByteSize),
		Width: int32(blob.Width), Height: int32(blob.Height), Content: blob.Content, UploadedByDevice: device,
	})
	if err != nil {
		return false, reportErr("media store", err)
	}
	return rows == 1, nil
}

var _ media.Repository = MediaBlobs{}
