package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

type fakeRepo struct{ stored map[string]Blob }

func (f *fakeRepo) StoreBlob(_ context.Context, blob Blob, _ string) (bool, error) {
	if _, ok := f.stored[blob.SHA256]; ok {
		return false, nil
	}
	f.stored[blob.SHA256] = blob
	return true, nil
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func kindOf(err error) (apperr.Kind, string) {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Kind, ae.Message
	}
	return 0, ""
}

func TestUploadStoresOnceAndRecordsDimensions(t *testing.T) {
	repo := &fakeRepo{stored: map[string]Blob{}}
	svc := NewService(repo)
	data := pngBytes(t)
	created, blob, err := svc.Upload(context.Background(), "dev", hashOf(data), "image/png", data)
	if err != nil || !created || blob.Width != 4 || blob.Height != 3 || blob.ByteSize != len(data) {
		t.Fatalf("first upload: created=%v blob=%+v err=%v", created, blob, err)
	}
	created, _, err = svc.Upload(context.Background(), "dev", hashOf(data), "image/png", data)
	if err != nil || created {
		t.Fatalf("repeat upload must be already stored: created=%v err=%v", created, err)
	}
}

func TestUploadRejectsMismatchesAndNonImages(t *testing.T) {
	svc := NewService(&fakeRepo{stored: map[string]Blob{}})
	data := pngBytes(t)
	cases := []struct {
		name, hash, contentType string
		body                    []byte
		kind                    apperr.Kind
		message                 string
	}{
		{"hash mismatch", hashOf([]byte("other")), "image/png", data, apperr.Unprocessable, CodeHashMismatch},
		{"wrong declared type", hashOf(data), "image/jpeg", data, apperr.Unprocessable, CodeInvalid},
		{"not an image", hashOf([]byte("hello")), "image/png", []byte("hello"), apperr.Unprocessable, CodeInvalid},
		{"bad hash path", "ABC", "image/png", data, apperr.InvalidInput, ""},
		{"gif", hashOf(data), "image/gif", data, apperr.UnsupportedMedia, ""},
	}
	for _, tc := range cases {
		_, _, err := svc.Upload(context.Background(), "dev", tc.hash, tc.contentType, tc.body)
		kind, message := kindOf(err)
		if kind != tc.kind || (tc.message != "" && message != tc.message) {
			t.Errorf("%s: got kind=%v message=%q", tc.name, kind, message)
		}
	}
}
