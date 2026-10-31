package app

import (
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

func TestMediaServiceConstructionIsExplicitAndLocal(t *testing.T) {
	// Construct without a repository: no database/network operation may occur.
	if _, err := NewMediaService(config.MediaConfig{Storage: "local", LocalDir: t.TempDir()}, nil); err != nil {
		t.Fatal(err)
	}
	cfg := config.MediaConfig{Storage: "r2", Endpoint: "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com", Bucket: "moonlight-media-dev", AccessKeyID: "synthetic-access", SecretAccessKey: strings.Repeat("b", 64)}
	if _, err := NewMediaService(cfg, nil); err != nil {
		t.Fatal(err)
	}
	cfg.SecretAccessKey = ""
	if _, err := NewMediaService(cfg, nil); err == nil {
		t.Fatal("missing R2 credentials silently disabled uploads")
	}
	// Unresolved storage never falls back to anything.
	if _, err := NewMediaService(config.MediaConfig{}, nil); err == nil {
		t.Fatal("unconfigured image storage accepted")
	}
}
