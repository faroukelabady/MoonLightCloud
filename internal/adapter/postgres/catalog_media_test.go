package postgres

// ADR-0056: Product media projection and content-addressed blob storage
// through the real projector, Store bindings and schema.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

func mediaPayload(productID string, revision int64, images ...map[string]any) string {
	if images == nil {
		images = []map[string]any{}
	}
	raw, _ := json.Marshal(map[string]any{"product_id": productID, "media_revision": revision, "images": images})
	return string(raw)
}

func mediaImage(imageID string, variantID any, position int, primary bool, hashByte string) map[string]any {
	return map[string]any{
		"image_id": imageID, "variant_id": variantID, "position": position, "is_primary": primary,
		"sha256": strings.Repeat(hashByte, 64), "content_type": "image/jpeg",
		"byte_size": 2048, "width": 1200, "height": 900,
	}
}

func TestProductMediaProjection(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	seq := 0
	ingest := func(dev, cred, typ, payload string) string {
		event := fmt.Sprintf("e3d1a000-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, dev, cred, event, typ, payload)
		return event
	}
	project := func(dev, cred, typ, payload string) catalog.ProjectResult {
		event := ingest(dev, cred, typ, payload)
		return projectCatalogTerminal(t, f, event, typ)
	}
	want := func(result catalog.ProjectResult, outcome catalog.Outcome) {
		t.Helper()
		if result.Outcome != outcome {
			t.Fatalf("outcome %+v want %v", result, outcome)
		}
	}
	productA := "33333333-0000-4000-8000-0000000000a1"
	productB := "33333333-0000-4000-8000-0000000000b1"
	catA := "aaaaaaaa-0000-4000-8000-0000000000a2"
	catB := "aaaaaaaa-0000-4000-8000-0000000000b2"
	variantA := "44444444-0000-4000-8000-0000000000a1"
	imageP := "55555555-0000-4000-8000-000000000001"
	imageV := "55555555-0000-4000-8000-000000000002"

	want(project(f.devA, f.credA, catalog.EventCategorySnapshotV2,
		categoryPayload(catA, "active", map[string]string{"en": "RootA"}, nil, 1)), catalog.OutcomeProcessed)
	want(project(f.devB, f.credB, catalog.EventCategorySnapshotV2,
		categoryPayload(catB, "active", map[string]string{"en": "RootB"}, nil, 1)), catalog.OutcomeProcessed)
	want(project(f.devA, f.credA, catalog.EventProductSnapshotV2,
		productPayload(productA, "ML-MEDIA-A", "A", catA, nil, nil, 1)), catalog.OutcomeProcessed)
	want(project(f.devB, f.credB, catalog.EventProductSnapshotV2,
		productPayload(productB, "ML-MEDIA-B", "B", catB, nil, nil, 1)), catalog.OutcomeProcessed)

	projection := NewCatalogProjection(f.pool, 5*time.Second)
	// A Variant gallery waits for its Variant instead of guessing.
	waiting := ingest(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(productA, 1,
		mediaImage(imageP, nil, 0, true, "a"), mediaImage(imageV, variantA, 0, true, "b")))
	record, ok, err := projection.LoadCatalogEvent(ctx, waiting)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if result, _ := projection.ProjectProductMedia(ctx, record, time.Now()); result.ErrorCode != ErrCatalogDependencyWait {
		t.Fatalf("missing variant must wait, got %+v", result)
	}
	want(project(f.devA, f.credA, catalog.EventProductVariantSnapshotV1,
		variantPayload(variantA, productA, "ML-MEDIA-A-1", true, false, "", []any{}, 1)), catalog.OutcomeProcessed)
	if err := projection.ResetProcessing(ctx, catalog.ProcessorProductMediaProjectionV1, waiting); err != nil {
		t.Fatalf("rearm: %v", err)
	}
	want(projectCatalogTerminal(t, f, waiting, catalog.EventProductMediaSnapshotV1), catalog.OutcomeProcessed)

	rows := func(product string) int {
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_product_media WHERE product_id = $1`, product).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	revision := func(product string) int64 {
		var n int64
		if err := f.pool.QueryRow(ctx, `SELECT media_revision FROM catalog_products WHERE product_id = $1`, product).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if rows(productA) != 2 || revision(productA) != 1 {
		t.Fatalf("rev 1: rows=%d revision=%d", rows(productA), revision(productA))
	}
	// Equal revision: identical replay is fine, different content blocks.
	want(project(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(productA, 1,
		mediaImage(imageV, variantA, 0, true, "b"), mediaImage(imageP, nil, 0, true, "a"))), catalog.OutcomeProcessed)
	want(project(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(productA, 1,
		mediaImage(imageP, nil, 0, true, "c"))), catalog.OutcomeBlocked)
	// Newer revision replaces the galleries; an older one is a no-op.
	want(project(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(productA, 2,
		mediaImage(imageP, nil, 0, true, "d"))), catalog.OutcomeProcessed)
	want(project(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(productA, 1,
		mediaImage(imageP, nil, 0, true, "a"), mediaImage(imageV, variantA, 0, true, "b"))), catalog.OutcomeProcessed)
	if rows(productA) != 1 || revision(productA) != 2 {
		t.Fatalf("rev 2: rows=%d revision=%d", rows(productA), revision(productA))
	}
	// Another Store cannot write A's galleries, and an image ID never moves.
	want(project(f.devB, f.credB, catalog.EventProductMediaSnapshotV1, mediaPayload(productA, 3)), catalog.OutcomeBlocked)
	want(project(f.devB, f.credB, catalog.EventProductMediaSnapshotV1, mediaPayload(productB, 1,
		mediaImage(imageP, nil, 0, true, "d"))), catalog.OutcomeBlocked)
	// A Variant of another Product is never attached.
	want(project(f.devB, f.credB, catalog.EventProductMediaSnapshotV1, mediaPayload(productB, 1,
		mediaImage("55555555-0000-4000-8000-000000000003", variantA, 0, true, "e"))), catalog.OutcomeBlocked)
	// Removing every image is a valid state.
	want(project(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(productA, 3)), catalog.OutcomeProcessed)
	if rows(productA) != 0 || revision(productA) != 3 {
		t.Fatalf("rev 3: rows=%d revision=%d", rows(productA), revision(productA))
	}
}

func TestMediaBlobsStoreOnce(t *testing.T) {
	f := openScopeFixture(t)
	blobs := NewMediaBlobs(f.pool, 5*time.Second)
	blob := media.Blob{SHA256: strings.Repeat("f", 64), ContentType: "image/png", ByteSize: 3, Width: 1, Height: 1, Content: []byte{1, 2, 3}}
	created, err := blobs.StoreBlob(context.Background(), blob, f.devA)
	if err != nil || !created {
		t.Fatalf("first store: %v %v", created, err)
	}
	created, err = blobs.StoreBlob(context.Background(), blob, f.devB)
	if err != nil || created {
		t.Fatalf("repeat store: %v %v", created, err)
	}
	var device string
	if err := f.pool.QueryRow(context.Background(), `SELECT uploaded_by_device::text FROM media_blobs WHERE sha256 = $1`, blob.SHA256).Scan(&device); err != nil || device != f.devA {
		t.Fatalf("first uploader kept: %q %v", device, err)
	}
}
