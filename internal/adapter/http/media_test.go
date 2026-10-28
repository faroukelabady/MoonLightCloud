package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

type memoryBlobs map[string]media.Blob

func (m memoryBlobs) StoreBlob(_ context.Context, blob media.Blob, _ string) (bool, error) {
	if _, ok := m[blob.SHA256]; ok {
		return false, nil
	}
	m[blob.SHA256] = blob
	return true, nil
}

func TestMediaUploadRoute(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.Handle("PUT /api/v1/sync/media/{sha256}", MediaUpload(media.NewService(memoryBlobs{})))
	put := func(path, contentType string, body []byte, device bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		if device {
			req = withDevice(req, "11111111-1111-4111-8111-111111111111")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := put("/api/v1/sync/media/"+hash, "image/png", data, true); rec.Code != http.StatusCreated {
		t.Fatalf("first upload: %d %s", rec.Code, rec.Body)
	}
	rec := put("/api/v1/sync/media/"+hash, "image/png", data, true)
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusOK || body["status"] != "already_stored" {
		t.Fatalf("repeat upload: %d %v", rec.Code, body)
	}
	if rec := put("/api/v1/sync/media/"+strings.Repeat("0", 64), "image/png", data, true); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "MEDIA_HASH_MISMATCH") {
		t.Fatalf("hash mismatch: %d %s", rec.Code, rec.Body)
	}
	if rec := put("/api/v1/sync/media/"+hash, "image/gif", data, true); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("gif: %d", rec.Code)
	}
	if rec := put("/api/v1/sync/media/"+hash, "image/png", make([]byte, 5<<20+1), true); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d", rec.Code)
	}
	if rec := put("/api/v1/sync/media/"+hash, "image/png", data, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no device: %d", rec.Code)
	}
}
