// Package media accepts Retail product image bytes (ADR-0056). Files are
// content-addressed: the SHA-256 of the body is the identity, so a repeat
// upload is harmless and order relative to the media snapshot never matters.
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Repository stores blobs. Store reports false when the hash already existed.
type Repository interface {
	StoreBlob(ctx context.Context, blob Blob, deviceID string) (bool, error)
}

// Service verifies and stores uploads.
type Service struct{ repo Repository }

// NewService builds the upload service.
func NewService(repo Repository) Service { return Service{repo: repo} }

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
	created, err = s.repo.StoreBlob(ctx, blob, deviceID)
	if err != nil {
		return false, Blob{}, err
	}
	return created, blob, nil
}
