package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"strings"
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

func (f *fakeRepo) BlobInfo(_ context.Context, hash string) (Blob, error) {
	blob, ok := f.stored[hash]
	if !ok {
		return Blob{}, apperr.New(apperr.NotFound, "media file not stored")
	}
	blob.Content = nil
	return blob, nil
}

// memoryObjects is an in-memory ObjectStore.
type memoryObjects map[string][]byte

func (m memoryObjects) PutImage(_ context.Context, b Blob) error {
	m[b.SHA256] = append([]byte{}, b.Content...)
	return nil
}

func (m memoryObjects) GetImage(_ context.Context, hash string) ([]byte, error) {
	data, ok := m[hash]
	if !ok {
		return nil, apperr.New(apperr.NotFound, "object not found")
	}
	return data, nil
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
	svc := NewService(repo, memoryObjects{})
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
	svc := NewService(&fakeRepo{stored: map[string]Blob{}}, memoryObjects{})
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

type objectStoreFunc func(context.Context, Blob) error

func (f objectStoreFunc) PutImage(ctx context.Context, b Blob) error { return f(ctx, b) }
func (objectStoreFunc) GetImage(context.Context, string) ([]byte, error) {
	return nil, apperr.New(apperr.NotFound, "object not found")
}

type repoFunc func(context.Context, Blob, string) (bool, error)

func (f repoFunc) StoreBlob(ctx context.Context, b Blob, id string) (bool, error) {
	return f(ctx, b, id)
}

func (repoFunc) BlobInfo(context.Context, string) (Blob, error) {
	return Blob{}, apperr.New(apperr.NotFound, "media file not stored")
}

func TestObjectStorageMustSucceedBeforeDurableAck(t *testing.T) {
	data := pngBytes(t)
	hash := hashOf(data)
	ctx := context.Background()
	objects := map[string][]byte{}
	failObject, failDB := true, false
	attempts, dbCalls := 0, 0
	repo := repoFunc(func(_ context.Context, b Blob, _ string) (bool, error) {
		dbCalls++
		if !bytes.Equal(objects[b.SHA256], b.Content) {
			t.Fatal("database write preceded R2")
		}
		if failDB {
			return false, errors.New("synthetic database failure")
		}
		return true, nil
	})
	store := objectStoreFunc(func(_ context.Context, b Blob) error {
		attempts++
		if failObject {
			return errors.New("private SDK diagnostic and credentials")
		}
		objects[b.SHA256] = append([]byte{}, b.Content...)
		return nil
	})
	svc := NewService(repo, store)
	created, _, err := svc.Upload(ctx, "dev", hash, "image/png", data)
	if kind, _ := kindOf(err); kind != apperr.Unavailable || created || dbCalls != 0 || len(objects) != 0 || strings.Contains(err.Error(), "private SDK") {
		t.Fatal("R2 failure was acknowledged, persisted or exposed")
	}
	failObject = false
	failDB = true
	if created, _, err = svc.Upload(ctx, "dev", hash, "image/png", data); err == nil || created || len(objects) != 1 {
		t.Fatal("database failure falsely acknowledged or discarded object")
	}
	failDB = false
	if created, _, err = svc.Upload(ctx, "dev", hash, "image/png", data); err != nil || !created || len(objects) != 1 || attempts != 3 {
		t.Fatal("same-hash retry did not converge")
	}
	attemptsBefore := attempts
	if _, _, err = svc.Upload(ctx, "dev", hashOf([]byte("wrong")), "image/png", data); err == nil || attempts != attemptsBefore {
		t.Fatal("invalid image reached R2")
	}
}

// Reads come from object storage and are refused unless the bytes still
// match the recorded size and hash.
func TestReadVerifiesObjectBytes(t *testing.T) {
	data := pngBytes(t)
	hash := hashOf(data)
	ctx := context.Background()
	repo := &fakeRepo{stored: map[string]Blob{}}
	objects := memoryObjects{}
	svc := NewService(repo, objects)
	if _, err := svc.Read(ctx, hash); err == nil {
		t.Fatal("a file never uploaded was read")
	}
	if _, _, err := svc.Upload(ctx, "dev", hash, "image/png", data); err != nil {
		t.Fatal(err)
	}
	blob, err := svc.Read(ctx, hash)
	if err != nil || !bytes.Equal(blob.Content, data) || blob.ContentType != "image/png" || blob.Width != 4 {
		t.Fatalf("read: %+v %v", blob, err)
	}
	if _, err := svc.Read(ctx, "../etc/passwd"); err == nil {
		t.Fatal("invalid hash read")
	}
	objects[hash] = append([]byte{}, data[:len(data)-1]...)
	objects[hash] = append(objects[hash], 'x')
	if _, err := svc.Read(ctx, hash); err == nil {
		t.Fatal("tampered object served")
	} else if kind, _ := kindOf(err); kind != apperr.Unavailable {
		t.Fatalf("tampered object kind %v", kind)
	}
	delete(objects, hash)
	if _, err := svc.Read(ctx, hash); err == nil {
		t.Fatal("missing object served")
	} else if kind, _ := kindOf(err); kind != apperr.Unavailable {
		t.Fatalf("missing object kind %v", kind)
	}
}
