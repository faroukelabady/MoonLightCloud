package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

func TestStoreWritesReadsAndRefusesBadKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("image-bytes")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	ctx := context.Background()
	var ae *apperr.Error
	if _, err := store.GetImage(ctx, hash); !errors.As(err, &ae) || ae.Kind != apperr.NotFound {
		t.Fatalf("missing file: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.PutImage(ctx, media.Blob{SHA256: hash, ContentType: "image/png", ByteSize: len(data), Content: data}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetImage(ctx, hash)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "product-images", "sha256", hash[:2], hash)); err != nil {
		t.Fatalf("same key layout as R2: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "product-images", "sha256", hash[:2]))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %d", len(entries))
	}
	if err := store.PutImage(ctx, media.Blob{SHA256: "../../escape", Content: data}); err == nil {
		t.Fatal("path escape written")
	}
	if _, err := store.GetImage(ctx, "../../escape"); err == nil {
		t.Fatal("path escape read")
	}
}

func TestStoreHasImage(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("probe-bytes")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	ctx := context.Background()
	if found, err := store.HasImage(ctx, hash); err != nil || found {
		t.Fatalf("before upload: %v %v", found, err)
	}
	if err := store.PutImage(ctx, media.Blob{SHA256: hash, ContentType: "image/png", ByteSize: len(data), Content: data}); err != nil {
		t.Fatal(err)
	}
	if found, err := store.HasImage(ctx, hash); err != nil || !found {
		t.Fatalf("after upload: %v %v", found, err)
	}
	if _, err := store.HasImage(ctx, "../etc"); err == nil {
		t.Fatal("a bad key must fail")
	}
}
