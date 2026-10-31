// Package r2 contains the S3-compatible Cloudflare adapter. Domain code never
// sees the SDK, credentials, endpoint or bucket.
package r2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

// Leave time in the API's normal write budget for the database commit/ACK;
// reads use the same bound.
const uploadTimeout = 8 * time.Second

var (
	errUpload   = errors.New("R2 image upload failed")
	errDownload = apperr.New(apperr.Unavailable, "R2 image download failed")
	errMissing  = apperr.New(apperr.NotFound, "R2 image object not found")
)

type Store struct {
	client *s3.Client
	bucket string
}

// New performs no network I/O. An explicit static credential provider prevents
// AWS environment/instance credentials from being used as a silent fallback.
func New(cfg config.MediaConfig, transport *http.Client) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Storage != "r2" {
		return nil, errors.New("R2 image storage is not enabled")
	}
	client := http.Client{Timeout: uploadTimeout}
	if transport != nil {
		client = *transport
	}
	if client.Timeout <= 0 || client.Timeout > uploadTimeout {
		client.Timeout = uploadTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	awsCfg := aws.Config{
		Region:                     "auto",
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		HTTPClient:                 &client,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		Retryer: func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 2; o.MaxBackoff = time.Second })
		},
	}
	return &Store{bucket: cfg.Bucket, client: s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
	})}, nil
}

// PutImage overwrites only this verified content hash. A lost ACK or database
// failure can safely retry the same bytes; no R2 object is deleted on failure.
func (s *Store) PutImage(ctx context.Context, blob media.Blob) error {
	if !catalog.ValidSHA256(blob.SHA256) || !catalog.ValidMediaContentType(blob.ContentType) || len(blob.Content) < 1 || len(blob.Content) > catalog.MaxMediaBytes || len(blob.Content) != blob.ByteSize {
		return errUpload
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(objectKey(blob.SHA256)),
		ContentType: aws.String(blob.ContentType), ContentLength: aws.Int64(int64(blob.ByteSize)),
		Body: bytes.NewReader(blob.Content),
	})
	if err != nil {
		return errUpload
	} // SDK errors can contain signed request details.
	return nil
}

// GetImage reads one object by its hash, at most MaxMediaBytes+1 bytes so
// the caller's size check refuses an oversized object. A missing object is
// NotFound; every other failure is one fixed Unavailable error.
func (s *Store) GetImage(ctx context.Context, sha256 string) ([]byte, error) {
	if !catalog.ValidSHA256(sha256) {
		return nil, errDownload
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey(sha256)),
	})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, errMissing
		}
		return nil, errDownload
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, catalog.MaxMediaBytes+1))
	if err != nil {
		return nil, errDownload
	}
	return data, nil
}

// objectKey derives the key from the verified hash only, never a filename.
func objectKey(sha256 string) string {
	return "product-images/sha256/" + sha256[:2] + "/" + sha256
}

var _ media.ObjectStore = (*Store)(nil)
