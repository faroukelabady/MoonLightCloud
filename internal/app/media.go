package app

import (
	"context"
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/filemedia"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/r2"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

// newMediaObjects builds the image byte store (ADR-0057). It performs no
// network I/O; config.Validate has already refused local storage outside
// development.
func newMediaObjects(cfg config.MediaConfig) (media.ObjectStore, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Storage {
	case "r2":
		return r2.New(cfg, nil)
	case "local":
		return filemedia.New(cfg.LocalDir)
	default:
		return nil, errors.New("image storage is not configured")
	}
}

// NewMediaService is the one image service: device uploads, the dashboard
// and online channels all read and write through it.
func NewMediaService(cfg config.MediaConfig, repo media.Repository) (media.Service, error) {
	objects, err := newMediaObjects(cfg)
	if err != nil {
		return media.Service{}, err
	}
	return media.NewService(repo, objects), nil
}

// mediaReaders adapts the verified image read to the commerce and catalog
// admin ports.
type mediaReaders struct{ svc media.Service }

func (m mediaReaders) MediaContent(ctx context.Context, sha256 string) (commerce.MediaContent, error) {
	blob, err := m.svc.Read(ctx, sha256)
	if err != nil {
		return commerce.MediaContent{}, err
	}
	return commerce.MediaContent{SHA256: blob.SHA256, ContentType: blob.ContentType, Bytes: blob.Content}, nil
}

func (m mediaReaders) ReadImage(ctx context.Context, sha256 string) (catalogadmin.AdminMedia, error) {
	blob, err := m.svc.Read(ctx, sha256)
	if err != nil {
		return catalogadmin.AdminMedia{}, err
	}
	return catalogadmin.AdminMedia{ContentType: blob.ContentType, Content: blob.Content}, nil
}

// MediaContentReader exposes the verified read to the commerce service.
func MediaContentReader(svc media.Service) commerce.MediaContentReader { return mediaReaders{svc} }

var (
	_ commerce.MediaContentReader = mediaReaders{}
	_ catalogadmin.ImageReader    = mediaReaders{}
)
