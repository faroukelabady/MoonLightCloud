package app

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

func TestInvalidTUFConfigurationCannotUseLegacyImportKeys(t *testing.T) {
	cfg := config.Config{ReleaseTUFBootstrap: t.TempDir() + "/missing-public-bootstrap.json", ReleaseTUFCacheDir: t.TempDir(), ReleaseTrustedPublicKeys: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	handlers := newUpdateHandlers(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest("POST", "/api/v1/dashboard/releases", strings.NewReader(`{"envelope":{},"artifact_urls":{}}`))
	resp := httptest.NewRecorder()
	handlers.ImportRelease(resp, req)
	if !strings.Contains(resp.Body.String(), "RELEASE_TRUST_NOT_CONFIGURED") {
		t.Fatalf("invalid TUF setup did not disable import: %d", resp.Code)
	}
}
