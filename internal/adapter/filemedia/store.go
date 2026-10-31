// Package filemedia keeps product image bytes in a local directory. It is the
// development stand-in for R2 (ADR-0057); configuration refuses it outside
// development, so no deployed Cloud ever keeps images on its own disk.
package filemedia

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

var (
	errWrite   = errors.New("local image write failed")
	errRead    = apperr.New(apperr.Unavailable, "local image read failed")
	errMissing = apperr.New(apperr.NotFound, "local image not found")
)

// Store writes one file per hash below Dir, using the same key layout as R2.
type Store struct{ dir string }

// New creates the directory (owner-only) when it does not exist yet.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("local image directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("local image directory cannot be created")
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(sha256 string) string {
	return filepath.Join(s.dir, "product-images", "sha256", sha256[:2], sha256)
}

// PutImage writes through a temporary file and renames it into place, so a
// reader never sees a partly written image.
func (s *Store) PutImage(ctx context.Context, blob media.Blob) error {
	if !catalog.ValidSHA256(blob.SHA256) || len(blob.Content) < 1 || len(blob.Content) > catalog.MaxMediaBytes {
		return errWrite
	}
	if err := ctx.Err(); err != nil {
		return errWrite
	}
	target := s.path(blob.SHA256)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return errWrite
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return errWrite
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, writeErr := tmp.Write(blob.Content)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		return errWrite
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return errWrite
	}
	return nil
}

// GetImage reads at most MaxMediaBytes+1 bytes of one stored file.
func (s *Store) GetImage(ctx context.Context, sha256 string) ([]byte, error) {
	if !catalog.ValidSHA256(sha256) {
		return nil, errRead
	}
	if err := ctx.Err(); err != nil {
		return nil, errRead
	}
	file, err := os.Open(s.path(sha256))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errMissing
		}
		return nil, errRead
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, catalog.MaxMediaBytes+1))
	if err != nil {
		return nil, errRead
	}
	return data, nil
}

// HasImage reports whether one file is stored.
func (s *Store) HasImage(ctx context.Context, sha256 string) (bool, error) {
	if !catalog.ValidSHA256(sha256) {
		return false, errRead
	}
	if err := ctx.Err(); err != nil {
		return false, errRead
	}
	if _, err := os.Stat(s.path(sha256)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, errRead
	}
	return true, nil
}

var _ media.ObjectStore = (*Store)(nil)
