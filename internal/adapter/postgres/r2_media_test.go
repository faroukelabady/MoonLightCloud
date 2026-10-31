package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	adapterhttp "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	postgres "github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/r2"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type r2TestTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (x r2TestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = x.target.Scheme
	u.Host = x.target.Host
	copy.URL = &u
	return x.base.RoundTrip(copy)
}

// Full production boundary: authenticated HTTP -> media validation -> real
// signed S3 adapter -> PostgreSQL record, then the verified read back. Only
// the external R2 server is a TLS fixture.
func TestR2MediaUploadAckAndReadBack(t *testing.T) {
	pool, auth := openR2TestRepo(t)
	ctx := context.Background()
	device, err := auth.Create(ctx, "R2 fixture device")
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	imageBytes := data.Bytes()
	sum := sha256.Sum256(imageBytes)
	hash := hex.EncodeToString(sum[:])
	var mu sync.Mutex
	objects := map[string][]byte{}
	failR2 := true
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			b, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			_, _ = w.Write(b)
			return
		}
		if failR2 {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>private R2 provider details</Message></Error>`)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		objects[r.URL.Path] = append([]byte{}, b...)
		w.WriteHeader(200)
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	client := backend.Client()
	client.Transport = r2TestTransport{target: u, base: client.Transport}
	cfg := config.MediaConfig{Storage: "r2", Endpoint: "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com", Bucket: "moonlight-media-dev", AccessKeyID: "synthetic-access", SecretAccessKey: strings.Repeat("b", 64)}
	objectStore, err := r2.New(cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	svc := media.NewService(postgres.NewMediaBlobs(pool, 5*time.Second), objectStore)
	mux := http.NewServeMux()
	mux.Handle("PUT /api/v1/sync/media/{sha256}", adapterhttp.DeviceAuth(auth)(adapterhttp.MediaUpload(svc)))
	put := func(credential, claim string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/v1/sync/media/"+claim, bytes.NewReader(imageBytes))
		req.Header.Set("Content-Type", "image/png")
		req.Header.Set("Authorization", credential)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	token := "Bearer " + device.Device.ID + "." + device.Credential.ID + "." + device.RawSecret
	count := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_blobs WHERE sha256=$1`, hash).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if rec := put(token, hash); rec.Code != 503 || count() != 0 || strings.Contains(rec.Body.String(), "private R2") {
		t.Fatalf("R2 failure must be safe and retryable: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	failR2 = false
	mu.Unlock()
	if rec := put("", hash); rec.Code != 401 {
		t.Fatal("unauthenticated upload accepted")
	}
	if rec := put(token, strings.Repeat("0", 64)); rec.Code != 422 {
		t.Fatal("wrong hash accepted")
	}
	mu.Lock()
	n := len(objects)
	mu.Unlock()
	if n != 0 {
		t.Fatal("invalid requests reached R2")
	}
	// Simulate DB failure after successful R2 storage. No compensation deletes
	// the immutable object; retry converges without a second object or blob.
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_r2_blob() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic storage failure'; END $$; CREATE TRIGGER reject_r2_blob BEFORE INSERT ON media_blobs FOR EACH ROW EXECUTE FUNCTION reject_r2_blob()`)
	if err != nil {
		t.Fatal(err)
	}
	if rec := put(token, hash); rec.Code != 500 || count() != 0 {
		t.Fatalf("DB failure ACK: %d", rec.Code)
	}
	mu.Lock()
	n = len(objects)
	mu.Unlock()
	if n != 1 {
		t.Fatal("R2 object lost after database failure")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_r2_blob ON media_blobs; DROP FUNCTION reject_r2_blob()`)
	if err != nil {
		t.Fatal(err)
	}
	if rec := put(token, hash); rec.Code != 201 || count() != 1 {
		t.Fatalf("retry ACK: %d %s", rec.Code, rec.Body)
	}
	if rec := put(token, hash); rec.Code != 200 || count() != 1 {
		t.Fatalf("repeat ACK: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if len(objects) != 1 {
		t.Error("repeat created more than one object")
	}
	for _, b := range objects {
		if !bytes.Equal(b, imageBytes) {
			t.Error("object bytes changed")
		}
	}
	mu.Unlock()
	// PostgreSQL records the file but never its bytes; readers get the R2
	// object back, verified against the recorded hash.
	var hasContent bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'media_blobs' AND column_name = 'content')`).Scan(&hasContent); err != nil || hasContent {
		t.Fatalf("media_blobs still stores image bytes: %v %v", hasContent, err)
	}
	blob, err := svc.Read(ctx, hash)
	if err != nil || !bytes.Equal(blob.Content, imageBytes) || blob.ContentType != "image/png" {
		t.Fatalf("read back from R2: %v", err)
	}
	mu.Lock()
	for key := range objects {
		objects[key] = append([]byte{}, imageBytes[:len(imageBytes)-1]...)
	}
	mu.Unlock()
	if _, err := svc.Read(ctx, hash); err == nil {
		t.Fatal("an object that no longer matches its hash was served")
	}
}

func openR2TestRepo(t *testing.T) (*pgxpool.Pool, auth.Service) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	hasher, err := auth.NewHasher(bytes.Repeat([]byte{'t'}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return pool, auth.NewService(postgres.NewDevices(pool, 5*time.Second), hasher, "", 1, clock.System{}, ids.System{})
}
