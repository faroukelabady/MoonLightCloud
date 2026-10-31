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
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
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

// ADR-0056 step 2: galleries and late files re-evaluate online channels,
// the commerce read reports file presence, and created provider media is
// recorded per Product and file.
func TestProductMediaCommerceState(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	seq := 0
	project := func(dev, cred, typ, payload string) {
		t.Helper()
		event := fmt.Sprintf("e3d1b000-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, dev, cred, event, typ, payload)
		if result := projectCatalogTerminal(t, f, event, typ); result.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("project %s: %+v", typ, result)
		}
	}
	product := "33333333-0000-4000-8000-0000000000c1"
	category := "aaaaaaaa-0000-4000-8000-0000000000c2"
	project(f.devA, f.credA, catalog.EventCategorySnapshotV2,
		categoryPayload(category, "active", map[string]string{"en": "Root"}, nil, 1))
	project(f.devA, f.credA, catalog.EventProductSnapshotV2,
		productPayload(product, "ML-MEDIA-C", "C", category, nil, nil, 1))

	reason := func() string {
		t.Helper()
		var value string
		err := f.pool.QueryRow(ctx, `SELECT reason FROM commerce_product_reevaluations WHERE product_id = $1`, product).Scan(&value)
		if err != nil {
			return ""
		}
		return value
	}
	clear := func() {
		if _, err := f.pool.Exec(ctx, `DELETE FROM commerce_product_reevaluations`); err != nil {
			t.Fatal(err)
		}
	}
	clear()
	project(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(product, 1,
		mediaImage("55555555-0000-4000-8000-0000000000c1", nil, 0, true, "c")))
	if got := reason(); got != "product_media" {
		t.Fatalf("gallery change reason = %q", got)
	}

	read := catalog.NewService(NewCatalogProjection(f.pool, 5*time.Second))
	images, err := read.GetProductMedia(ctx, product)
	if err != nil || len(images) != 1 || images[0].Available {
		t.Fatalf("before upload: %+v %v", images, err)
	}

	// The file arrives after the gallery: the Product is re-evaluated.
	clear()
	blobs := NewMediaBlobs(f.pool, 5*time.Second)
	sha := strings.Repeat("c", 64)
	if created, err := blobs.StoreBlob(ctx, media.Blob{SHA256: sha, ContentType: "image/jpeg", ByteSize: 3, Width: 1, Height: 1, Content: []byte{7, 8, 9}}, f.devA); err != nil || !created {
		t.Fatalf("store: %v %v", created, err)
	}
	if got := reason(); got != "product_media" {
		t.Fatalf("late file reason = %q", got)
	}
	if images, err = read.GetProductMedia(ctx, product); err != nil || !images[0].Available {
		t.Fatalf("after upload: %+v %v", images, err)
	}
	info, err := blobs.BlobInfo(ctx, sha)
	if err != nil || info.ContentType != "image/jpeg" || info.ByteSize != 3 || info.Content != nil {
		t.Fatalf("info: %+v %v", info, err)
	}
	if _, err := blobs.BlobInfo(ctx, strings.Repeat("0", 64)); err == nil {
		t.Fatal("missing file read succeeded")
	}

	// Created provider media, keyed by provider, Product and file.
	store := NewCommercePersistence(f.pool, 5*time.Second)
	mapping := commerce.MediaMapping{SHA256: sha, ExternalProductID: "4100", ExternalMediaID: "9001"}
	if err := store.CreateProductMediaMapping(ctx, "shopify-main", product, mapping); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProductMediaMapping(ctx, "shopify-main", product, mapping); err != nil {
		t.Fatalf("same row again: %v", err)
	}
	other := mapping
	other.ExternalMediaID = "9002"
	if err := store.CreateProductMediaMapping(ctx, "shopify-main", product, other); err == nil {
		t.Fatal("second media for the same file accepted")
	}
	if err := store.CreateProductMediaMapping(ctx, "shopify-main", "33333333-0000-4000-8000-0000000000ff", mapping); err == nil {
		t.Fatal("mapping for an unprojected product accepted")
	}
	var sameStore bool
	if err := f.pool.QueryRow(ctx, `SELECT m.store_id = p.store_id FROM commerce_media_mappings m
		JOIN catalog_products p ON p.product_id = m.product_id WHERE m.product_id = $1`, product).Scan(&sameStore); err != nil || !sameStore {
		t.Fatalf("mapping store is not the product store: %v", err)
	}
	if err := store.DeleteProductMediaMapping(ctx, "shopify-main", product, other); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.ListProductMediaMappings(ctx, "shopify-main", product); err != nil || len(rows) != 1 || rows[0] != mapping {
		t.Fatalf("compare-and-delete removed the wrong row: %+v %v", rows, err)
	}
	if err := store.DeleteProductMediaMapping(ctx, "shopify-main", product, mapping); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.ListProductMediaMappings(ctx, "shopify-main", product); err != nil || len(rows) != 0 {
		t.Fatalf("after delete: %+v %v", rows, err)
	}
}

// The catalog admin shows galleries and serves their files: the list
// thumbnail prefers a stored Product-gallery main image over Variant
// galleries and missing files, and a Store never reads another's files.
func TestCatalogAdminMedia(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	seq := 0
	project := func(dev, cred, typ, payload string) {
		t.Helper()
		event := fmt.Sprintf("e3d1c000-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, dev, cred, event, typ, payload)
		if result := projectCatalogTerminal(t, f, event, typ); result.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("project %s: %+v", typ, result)
		}
	}
	product := "33333333-0000-4000-8000-0000000000d1"
	variant := "44444444-0000-4000-8000-0000000000d1"
	category := "aaaaaaaa-0000-4000-8000-0000000000d2"
	project(f.devA, f.credA, catalog.EventCategorySnapshotV2,
		categoryPayload(category, "active", map[string]string{"en": "Root"}, nil, 1))
	project(f.devA, f.credA, catalog.EventProductSnapshotV2,
		productPayload(product, "ML-MEDIA-D", "D", category, nil, nil, 1))
	project(f.devA, f.credA, catalog.EventProductVariantSnapshotV1,
		variantPayload(variant, product, "ML-MEDIA-D-1", true, false, "", []any{}, 1))
	project(f.devA, f.credA, catalog.EventProductMediaSnapshotV1, mediaPayload(product, 1,
		mediaImage("55555555-0000-4000-8000-0000000000d1", nil, 1, false, "1"),
		mediaImage("55555555-0000-4000-8000-0000000000d2", nil, 0, true, "2"),
		mediaImage("55555555-0000-4000-8000-0000000000d3", variant, 0, true, "3")))

	admin := NewCatalogAdmin(f.pool, 5*time.Second)
	blobs := NewMediaBlobs(f.pool, 5*time.Second)
	store := func(hashByte string) {
		t.Helper()
		blob := media.Blob{SHA256: strings.Repeat(hashByte, 64), ContentType: "image/jpeg", ByteSize: 3, Width: 1, Height: 1, Content: []byte(hashByte + "..")}
		if _, err := blobs.StoreBlob(ctx, blob, f.devA); err != nil {
			t.Fatal(err)
		}
	}
	thumbnail := func() string {
		t.Helper()
		rows, err := admin.AdminProductList(ctx, scopeStoreA, "ML-MEDIA-D", "", 20)
		if err != nil || len(rows) != 1 {
			t.Fatalf("list: %+v %v", rows, err)
		}
		return rows[0].ThumbnailSHA256
	}
	if got := thumbnail(); got != "" {
		t.Fatalf("no file stored yet, thumbnail %q", got)
	}
	store("3")
	if got := thumbnail(); got != strings.Repeat("3", 64) {
		t.Fatalf("only the variant file arrived, thumbnail %q", got)
	}
	store("1")
	if got := thumbnail(); got != strings.Repeat("1", 64) {
		t.Fatalf("product gallery beats variant gallery, thumbnail %q", got)
	}
	store("2")
	if got := thumbnail(); got != strings.Repeat("2", 64) {
		t.Fatalf("main image first, thumbnail %q", got)
	}

	detail, err := admin.AdminProductDetail(ctx, scopeStoreA, product)
	if err != nil || len(detail.Images) != 3 {
		t.Fatalf("detail images: %+v %v", detail.Images, err)
	}
	if first := detail.Images[0]; first.VariantID != "" || !first.IsPrimary || !first.Available {
		t.Fatalf("product gallery main image first: %+v", first)
	}
	if last := detail.Images[2]; last.VariantID != variant {
		t.Fatalf("variant gallery entry: %+v", last)
	}

	allowed := func(storeID, hashByte string) bool {
		t.Helper()
		ok, err := admin.AdminMediaAllowed(ctx, storeID, strings.Repeat(hashByte, 64))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !allowed(scopeStoreA, "2") {
		t.Fatal("own stored file refused")
	}
	if allowed(scopeStoreB, "2") {
		t.Fatal("another Store may read the file")
	}
	if allowed(scopeStoreA, "4") {
		t.Fatal("a file that was never stored is allowed")
	}
	store("9")
	if allowed(scopeStoreA, "9") {
		t.Fatal("a file no gallery names is allowed")
	}
}
