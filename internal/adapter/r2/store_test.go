package r2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

type routeTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (r routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	clone.URL = &u
	return r.base.RoundTrip(clone)
}
func configForTest() config.MediaConfig {
	return config.MediaConfig{Storage: "r2", Endpoint: "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com", Bucket: "moonlight-media-dev", AccessKeyID: "synthetic-access", SecretAccessKey: strings.Repeat("b", 64)}
}
func testBlob() media.Blob {
	data := []byte("verified-domain-image-bytes")
	sum := sha256.Sum256(data)
	return media.Blob{SHA256: hex.EncodeToString(sum[:]), ContentType: "image/png", ByteSize: len(data), Content: data}
}
func testStore(t *testing.T, handler http.HandlerFunc) *Store {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Transport = routeTransport{target: target, base: client.Transport}
	store, err := New(configForTest(), client)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func TestPutImageUsesSignedBoundedS3Request(t *testing.T) {
	blob := testBlob()
	var calls atomic.Int32
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Method != "PUT" || r.URL.Path != "/moonlight-media-dev/product-images/sha256/"+blob.SHA256[:2]+"/"+blob.SHA256 {
			t.Errorf("unexpected operation %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("x-id") != "PutObject" || !bytes.Equal(data, blob.Content) || r.Header.Get("Content-Type") != "image/png" || r.ContentLength != int64(len(data)) {
			t.Errorf("object: len=%d want=%d equal=%v type=%q contentLength=%d encoding=%q", len(data), len(blob.Content), bytes.Equal(data, blob.Content), r.Header.Get("Content-Type"), r.ContentLength, r.Header.Get("Content-Encoding"))
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !strings.Contains(auth, "Credential=synthetic-access/") || !strings.Contains(auth, "/auto/s3/aws4_request") || (r.Header.Get("X-Amz-Content-Sha256") != blob.SHA256 && r.Header.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD") {
			t.Errorf("signature: algorithm=%v credential=%v scope=%v hash=%q", strings.HasPrefix(auth, "AWS4-HMAC-SHA256 "), strings.Contains(auth, "Credential=synthetic-access/"), strings.Contains(auth, "/auto/s3/aws4_request"), r.Header.Get("X-Amz-Content-Sha256"))
		}
		if strings.Contains(r.URL.String(), "synthetic-access") || strings.Contains(auth, configForTest().SecretAccessKey) {
			t.Error("credential secret exposed")
		}
		if r.Header.Get("X-Amz-Acl") != "" || r.Header.Get("X-Amz-Trailer") != "" {
			t.Error("unsupported/public upload options")
		}
		w.Header().Set("ETag", `"test-etag"`)
		w.WriteHeader(http.StatusOK)
	})
	for i := 0; i < 2; i++ {
		if err := store.PutImage(context.Background(), blob); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("replay must use same content-addressed object")
	}
}
func TestPutImageRetriesTransientFailureAndNeverFollowsRedirects(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTemporaryRedirect, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if status == 500 && n == 2 {
					w.WriteHeader(200)
					return
				}
				w.Header().Set("Location", "https://unexpected.invalid/leak")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>private-provider-diagnostic</Message></Error>`)
			})
			err := store.PutImage(context.Background(), testBlob())
			if status == 500 {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("retry result calls=%d err=%v", calls.Load(), err)
				}
			} else if err == nil || calls.Load() != 1 {
				t.Fatalf("terminal result calls=%d err=%v", calls.Load(), err)
			}
			if err != nil && strings.Contains(err.Error(), "private-provider-diagnostic") {
				t.Fatal("provider error exposed")
			}
		})
	}
}
func TestPutImageCancelledOrInvalidNeverUploads(t *testing.T) {
	var calls atomic.Int32
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.PutImage(ctx, testBlob()); err == nil {
		t.Fatal("cancelled upload succeeded")
	}
	bad := testBlob()
	bad.SHA256 = "../unexpected"
	if err := store.PutImage(context.Background(), bad); err == nil {
		t.Fatal("invalid object key accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled or invalid upload reached R2")
	}
}
func TestGetImageReadsBoundedObjectAndMapsMissing(t *testing.T) {
	blob := testBlob()
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("unexpected read %s", r.Method)
		}
		switch r.URL.Path {
		case "/moonlight-media-dev/product-images/sha256/" + blob.SHA256[:2] + "/" + blob.SHA256:
			_, _ = w.Write(blob.Content)
		case "/moonlight-media-dev/product-images/sha256/aa/" + strings.Repeat("a", 64):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>private-provider-diagnostic</Message></Error>`)
		case "/moonlight-media-dev/product-images/sha256/bb/" + strings.Repeat("b", 64):
			_, _ = w.Write(bytes.Repeat([]byte{1}, catalog.MaxMediaBytes+100))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>private-provider-diagnostic</Message></Error>`)
		}
	})
	ctx := context.Background()
	if data, err := store.GetImage(ctx, blob.SHA256); err != nil || !bytes.Equal(data, blob.Content) {
		t.Fatalf("read: %q %v", data, err)
	}
	var ae *apperr.Error
	if _, err := store.GetImage(ctx, strings.Repeat("a", 64)); !errors.As(err, &ae) || ae.Kind != apperr.NotFound {
		t.Fatalf("missing object: %v", err)
	}
	if data, err := store.GetImage(ctx, strings.Repeat("b", 64)); err != nil || len(data) != catalog.MaxMediaBytes+1 {
		t.Fatalf("oversized object must be cut at the limit: %d %v", len(data), err)
	}
	_, err := store.GetImage(ctx, strings.Repeat("c", 64))
	if !errors.As(err, &ae) || ae.Kind != apperr.Unavailable || strings.Contains(err.Error(), "private-provider-diagnostic") {
		t.Fatalf("denied read: %v", err)
	}
	if _, err := store.GetImage(ctx, "../escape"); err == nil {
		t.Fatal("invalid key read")
	}
}

func TestHasImageUsesHeadAndMapsMissing(t *testing.T) {
	blob := testBlob()
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("unexpected probe %s", r.Method)
		}
		switch r.URL.Path {
		case "/moonlight-media-dev/product-images/sha256/" + blob.SHA256[:2] + "/" + blob.SHA256:
			w.Header().Set("Content-Length", "27")
		case "/moonlight-media-dev/product-images/sha256/aa/" + strings.Repeat("a", 64):
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})
	ctx := context.Background()
	if found, err := store.HasImage(ctx, blob.SHA256); err != nil || !found {
		t.Fatalf("stored object: %v %v", found, err)
	}
	if found, err := store.HasImage(ctx, strings.Repeat("a", 64)); err != nil || found {
		t.Fatalf("missing object: %v %v", found, err)
	}
	var ae *apperr.Error
	if _, err := store.HasImage(ctx, strings.Repeat("c", 64)); !errors.As(err, &ae) || ae.Kind != apperr.Unavailable {
		t.Fatalf("denied probe: %v", err)
	}
	if _, err := store.HasImage(ctx, "../escape"); err == nil {
		t.Fatal("invalid key probe")
	}
}

func TestUsageReadsAllPagesWithoutDownloadingObjects(t *testing.T) {
	var calls atomic.Int32
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != "GET" || r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("max-keys") != "1000" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("not a bounded signed bucket listing")
		}
		w.Header().Set("Content-Type", "application/xml")
		if n == 1 {
			io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>page2</NextContinuationToken><Contents><Key>a</Key><Size>1024</Size></Contents></ListBucketResult>`)
		} else {
			if r.URL.Query().Get("continuation-token") != "page2" {
				t.Error("cursor not forwarded")
			}
			io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>b</Key><Size>2048</Size></Contents></ListBucketResult>`)
		}
	})
	got, err := store.Usage(context.Background())
	if err != nil || got.Files != 2 || got.Bytes != 3072 || !got.Complete || calls.Load() != 2 {
		t.Fatalf("usage %+v, calls %d, err %v", got, calls.Load(), err)
	}
}

func TestUsageFailsClosedAndCapsLargeBuckets(t *testing.T) {
	for _, mode := range []string{"denied", "malformed", "bad_cursor", "partial"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if mode == "denied" {
					w.WriteHeader(403)
					io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>secret-provider-detail</Message></Error>`)
					return
				}
				if mode == "malformed" {
					io.WriteString(w, `<ListBucketResult/>`)
					return
				}
				token := ""
				if mode == "partial" {
					token = fmt.Sprint(n)
				}
				io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>`+token+`</NextContinuationToken><Contents><Key>a</Key><Size>7</Size></Contents></ListBucketResult>`)
			})
			got, err := store.Usage(context.Background())
			if mode == "partial" {
				if err != nil || got.Complete || got.Files != 10 || got.Bytes != 70 || calls.Load() != 10 {
					t.Fatalf("unbounded/inexact listing: %+v %v calls=%d", got, err, calls.Load())
				}
			} else if err == nil || got.Files != 0 || strings.Contains(err.Error(), "secret-provider-detail") {
				t.Fatalf("failed usage falsely succeeded/leaked detail: %+v %v", got, err)
			}
		})
	}
}
