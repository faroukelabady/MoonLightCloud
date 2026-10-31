// Package media accepts Retail product image bytes (ADR-0056). Files are
// content-addressed: the SHA-256 of the body is the identity, so a repeat
// upload is harmless and order relative to the media snapshot never matters.
// The bytes live in object storage; PostgreSQL records which files arrived
// (ADR-0057).
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// Detail codes carried as the error message (the envelope code stays the
// generic kind code).
const (
	CodeHashMismatch = "MEDIA_HASH_MISMATCH"
	CodeInvalid      = "INVALID_MEDIA"
)

// Blob is one verified image file.
type Blob struct {
	SHA256      string
	ContentType string
	ByteSize    int
	Width       int
	Height      int
	Content     []byte
}

// Repository records stored files without their bytes. StoreBlob reports
// false when the hash already existed; BlobInfo is NotFound until a file
// is stored and returns no Content.
type Repository interface {
	StoreBlob(ctx context.Context, blob Blob, deviceID string) (bool, error)
	BlobInfo(ctx context.Context, sha256 string) (Blob, error)
}

// ObjectStore holds the bytes under their verified hash. GetImage is
// NotFound for a missing object and reads at most MaxMediaBytes+1 bytes.
type ObjectStore interface {
	PutImage(ctx context.Context, blob Blob) error
	GetImage(ctx context.Context, sha256 string) ([]byte, error)
}

// Service verifies and stores uploads.
type Service struct {
	repo    Repository
	objects ObjectStore
}

// NewService builds the upload and read service. Every upload writes the
// object before the database row, so a recorded file is always readable.
func NewService(repo Repository, objects ObjectStore) Service {
	return Service{repo: repo, objects: objects}
}

// Upload verifies body against the claimed hash and content type, then
// stores it. created is false for a repeat of an existing file.
func (s Service) Upload(ctx context.Context, deviceID, hash, contentType string, body []byte) (created bool, blob Blob, err error) {
	if !catalog.ValidSHA256(hash) {
		return false, Blob{}, apperr.New(apperr.InvalidInput, "sha256 must be 64 lower-case hex characters")
	}
	if !catalog.ValidMediaContentType(contentType) {
		return false, Blob{}, apperr.New(apperr.UnsupportedMedia, "content type must be image/jpeg or image/png")
	}
	if len(body) == 0 {
		return false, Blob{}, apperr.New(apperr.Unprocessable, CodeInvalid)
	}
	if len(body) > catalog.MaxMediaBytes {
		return false, Blob{}, apperr.New(apperr.TooLarge, "image exceeds the size limit")
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != hash {
		return false, Blob{}, apperr.New(apperr.Unprocessable, CodeHashMismatch)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || "image/"+format != contentType || !catalog.ValidMediaDimensions(config.Width, config.Height) {
		return false, Blob{}, apperr.New(apperr.Unprocessable, CodeInvalid)
	}
	blob = Blob{SHA256: hash, ContentType: contentType, ByteSize: len(body),
		Width: config.Width, Height: config.Height, Content: body}
	if err := s.objects.PutImage(ctx, blob); err != nil {
		return false, Blob{}, apperr.New(apperr.Unavailable, "image object storage is unavailable; retry the upload")
	}
	created, err = s.repo.StoreBlob(ctx, blob, deviceID)
	if err != nil {
		return false, Blob{}, err
	}
	return created, blob, nil
}

// Read returns one stored file with its bytes. Bytes that no longer match
// the recorded size and hash are refused, so object storage can never hand
// a reader something other than what the device uploaded.
func (s Service) Read(ctx context.Context, hash string) (Blob, error) {
	if !catalog.ValidSHA256(hash) {
		return Blob{}, apperr.New(apperr.InvalidInput, "sha256 must be 64 lower-case hex characters")
	}
	blob, err := s.repo.BlobInfo(ctx, hash)
	if err != nil {
		return Blob{}, err
	}
	data, err := s.objects.GetImage(ctx, hash)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Kind == apperr.NotFound {
			return Blob{}, apperr.New(apperr.Unavailable, "stored image is missing from object storage")
		}
		return Blob{}, apperr.New(apperr.Unavailable, "image object storage is unavailable")
	}
	sum := sha256.Sum256(data)
	if len(data) != blob.ByteSize || hex.EncodeToString(sum[:]) != hash {
		return Blob{}, apperr.New(apperr.Unavailable, "stored image does not match its hash")
	}
	blob.Content = data
	return blob, nil
}
