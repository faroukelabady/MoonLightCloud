package shopify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// ADR-0056 step 2: Product images on Shopify.

type memoryContent map[string]commerce.MediaContent

func (m memoryContent) MediaContent(_ context.Context, sha string) (commerce.MediaContent, error) {
	content, ok := m[sha]
	if !ok {
		return commerce.MediaContent{}, apperr.New(apperr.NotFound, "media file not stored")
	}
	return content, nil
}

type memoryRecorder struct {
	rows []commerce.MediaMapping
}

func (r *memoryRecorder) Record(_ context.Context, mapping commerce.MediaMapping) error {
	r.rows = append(r.rows, mapping)
	return nil
}

func (r *memoryRecorder) Forget(_ context.Context, mapping commerce.MediaMapping) error {
	kept := r.rows[:0]
	for _, row := range r.rows {
		if row != mapping {
			kept = append(kept, row)
		}
	}
	r.rows = kept
	return nil
}

type mediaFixture struct {
	h        *shopifyHarness
	provider *ShopifyProvider
	content  memoryContent
	recorder *memoryRecorder
	product  string // product GID
}

func newMediaFixture(t *testing.T) *mediaFixture {
	t.Helper()
	h := newHarness(t)
	f := &mediaFixture{
		h: h, provider: newTestProvider(t, h), content: memoryContent{},
		recorder: &memoryRecorder{}, product: "gid://shopify/Product/4100",
	}
	h.seedProduct(&fakeProduct{
		gid: f.product, status: "ACTIVE", title: "بردية",
		metafields: map[string]string{"moonlight.product_id": "prod-1", "moonlight.provider_key": "shopify-main"},
		variants: []*fakeVariant{
			{gid: "gid://shopify/ProductVariant/4201", sku: "PAP-1-S"},
			{gid: "gid://shopify/ProductVariant/4202", sku: "PAP-1-L"},
		},
	})
	return f
}

// image stores a file and returns its desired entry.
func (f *mediaFixture) image(label, contentType string) commerce.MediaImage {
	body := []byte("image-bytes-" + label)
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	f.content[sha] = commerce.MediaContent{SHA256: sha, ContentType: contentType, Bytes: body}
	return commerce.MediaImage{SHA256: sha, ContentType: contentType, ByteSize: len(body), Width: 800, Height: 600}
}

func (f *mediaFixture) request(images ...commerce.MediaImage) commerce.MediaSyncRequest {
	return commerce.MediaSyncRequest{
		ProviderKey: "shopify-main", ProductID: "prod-1", ExternalProductID: "4100",
		Alt: "بردية توت", Images: images,
		Mappings: append([]commerce.MediaMapping(nil), f.recorder.rows...),
		Content:  f.content, Recorder: f.recorder,
	}
}

func (f *mediaFixture) sync(t *testing.T, req commerce.MediaSyncRequest) commerce.MediaSyncResult {
	t.Helper()
	result, err := f.provider.SyncProductMedia(context.Background(), req)
	if err != nil {
		t.Fatalf("SyncProductMedia: %v", err)
	}
	return result
}

// mediaFor returns the recorded Shopify media GID of one file.
func (f *mediaFixture) mediaFor(t *testing.T, sha string) string {
	t.Helper()
	for _, row := range f.recorder.rows {
		if row.SHA256 == sha {
			return FormatGID(ResourceMediaImage, row.ExternalMediaID)
		}
	}
	t.Fatalf("no media recorded for %s", sha[:8])
	return ""
}

func TestMediaUploadsAttachesAndOrdersBeforeManualMedia(t *testing.T) {
	f := newMediaFixture(t)
	manual := "gid://shopify/MediaImage/900"
	f.h.media.files[manual] = &fakeFile{gid: manual, status: fileStatusReady}
	f.h.media.productMedia[f.product] = []string{manual}
	main, second := f.image("main", "image/jpeg"), f.image("second", "image/png")

	result := f.sync(t, f.request(main, second))
	if result != (commerce.MediaSyncResult{Created: 2, Attached: 2}) {
		t.Fatalf("result = %+v", result)
	}
	want := []string{f.mediaFor(t, main.SHA256), f.mediaFor(t, second.SHA256), manual}
	if got := f.h.mediaOrder(f.product); !reflect.DeepEqual(got, want) {
		t.Fatalf("media order = %v, want %v", got, want)
	}
	if len(f.h.media.uploads) != 2 {
		t.Fatalf("staged uploads = %d", len(f.h.media.uploads))
	}
	for _, upload := range f.h.media.uploads {
		if upload.token != "" {
			t.Fatal("the access token reached the staged upload target")
		}
		if upload.fields["policy"] != "signed-policy" {
			t.Fatal("staged upload parameters were not sent")
		}
	}
	if !bytes.Equal(f.h.media.uploads[1].content, f.content[second.SHA256].Bytes) || f.h.media.uploads[1].contentType != "image/png" {
		t.Fatal("uploaded bytes or type differ from the stored file")
	}
	file := f.h.media.files[f.mediaFor(t, main.SHA256)]
	if file.filename != "moonlight-"+main.SHA256[:16]+".jpg" || file.alt != "بردية توت" {
		t.Fatalf("file = %q alt %q", file.filename, file.alt)
	}
}

func TestMediaSecondRunSendsNoMutation(t *testing.T) {
	f := newMediaFixture(t)
	main, second := f.image("main", "image/jpeg"), f.image("second", "image/jpeg")
	f.sync(t, f.request(main, second))
	before := len(f.h.mediaMutations())

	result := f.sync(t, f.request(main, second))
	if result != (commerce.MediaSyncResult{}) {
		t.Fatalf("result = %+v", result)
	}
	if after := f.h.mediaMutations(); len(after) != before {
		t.Fatalf("converged run sent mutations %v", after[before:])
	}
}

func TestMediaReplacesRemovedImagesAndKeepsManualMedia(t *testing.T) {
	f := newMediaFixture(t)
	manual := "gid://shopify/MediaImage/900"
	f.h.media.files[manual] = &fakeFile{gid: manual, status: fileStatusReady}
	f.h.media.productMedia[f.product] = []string{manual}
	first, dropped, added := f.image("first", "image/jpeg"), f.image("dropped", "image/jpeg"), f.image("added", "image/jpeg")
	f.sync(t, f.request(first, dropped))
	droppedID := f.mediaFor(t, dropped.SHA256)

	// The new image becomes the main one.
	result := f.sync(t, f.request(added, first))
	if result.Deleted != 1 || result.Created != 1 {
		t.Fatalf("result = %+v", result)
	}
	if f.h.media.files[droppedID] != nil {
		t.Fatal("removed image still in Shopify")
	}
	want := []string{f.mediaFor(t, added.SHA256), f.mediaFor(t, first.SHA256), manual}
	if got := f.h.mediaOrder(f.product); !reflect.DeepEqual(got, want) {
		t.Fatalf("media order = %v, want %v", got, want)
	}
	if f.h.media.files[manual] == nil {
		t.Fatal("manual media deleted")
	}
	for _, row := range f.recorder.rows {
		if row.SHA256 == dropped.SHA256 {
			t.Fatal("removed image still recorded")
		}
	}
}

func TestMediaProcessingFileIsAttachedOnALaterRun(t *testing.T) {
	f := newMediaFixture(t)
	delay := mediaPollDelay
	mediaPollDelay = 0
	t.Cleanup(func() { mediaPollDelay = delay })
	f.h.media.newStatus = "PROCESSING"
	main := f.image("main", "image/jpeg")

	result := f.sync(t, f.request(main))
	if result.Created != 1 || result.Pending != 1 || result.Attached != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(f.recorder.rows) != 1 {
		t.Fatal("created file not recorded before it was ready")
	}
	f.h.media.files[f.mediaFor(t, main.SHA256)].status = fileStatusReady

	result = f.sync(t, f.request(main))
	if result != (commerce.MediaSyncResult{Attached: 1}) {
		t.Fatalf("second result = %+v", result)
	}
	if len(f.h.media.uploads) != 1 {
		t.Fatal("ready file uploaded twice")
	}
}

func TestMediaFailedFileIsNotUploadedAgain(t *testing.T) {
	f := newMediaFixture(t)
	f.h.media.newStatus = fileStatusFailed
	main := f.image("main", "image/jpeg")
	if result := f.sync(t, f.request(main)); result.Failed != 1 || result.Pending != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result := f.sync(t, f.request(main)); result.Failed != 1 || result.Created != 0 {
		t.Fatalf("second result = %+v", result)
	}
	if len(f.h.media.uploads) != 1 {
		t.Fatalf("uploads = %d", len(f.h.media.uploads))
	}
}

func TestMediaDeletedInShopifyIsUploadedAgain(t *testing.T) {
	f := newMediaFixture(t)
	main := f.image("main", "image/jpeg")
	f.sync(t, f.request(main))
	gone := f.mediaFor(t, main.SHA256)
	delete(f.h.media.files, gone)
	f.h.media.productMedia[f.product] = nil

	if result := f.sync(t, f.request(main)); result.Created != 1 || result.Attached != 1 {
		t.Fatalf("result = %+v", result)
	}
	if f.mediaFor(t, main.SHA256) == gone || len(f.recorder.rows) != 1 {
		t.Fatalf("recorded = %+v", f.recorder.rows)
	}
}

func TestMediaSetsVariantImages(t *testing.T) {
	f := newMediaFixture(t)
	main, small, large := f.image("main", "image/jpeg"), f.image("small", "image/jpeg"), f.image("large", "image/jpeg")
	req := f.request(main, small, large)
	req.VariantImages = map[string]string{"var-s": small.SHA256, "var-l": large.SHA256, "var-unmapped": main.SHA256}
	req.ExistingVariants = map[string]string{"var-s": "4201", "var-l": "4202"}

	f.sync(t, req)
	if got := f.h.media.variantMedia["gid://shopify/ProductVariant/4201"]; got != f.mediaFor(t, small.SHA256) {
		t.Fatalf("small variant media = %q", got)
	}
	if got := f.h.media.variantMedia["gid://shopify/ProductVariant/4202"]; got != f.mediaFor(t, large.SHA256) {
		t.Fatalf("large variant media = %q", got)
	}
	before := len(f.h.mediaMutations())
	req.Mappings = append([]commerce.MediaMapping(nil), f.recorder.rows...)
	f.sync(t, req)
	if len(f.h.mediaMutations()) != before {
		t.Fatal("variant images set again although unchanged")
	}
}

func TestMediaRefusesForeignProduct(t *testing.T) {
	f := newMediaFixture(t)
	f.h.products["4100"].metafields["moonlight.product_id"] = "another-product"
	_, err := f.provider.SyncProductMedia(context.Background(), f.request(f.image("main", "image/jpeg")))
	wantConflict(t, err)
	if len(f.h.mediaMutations()) != 0 || len(f.h.media.uploads) != 0 {
		t.Fatal("foreign product received media writes")
	}
}

func TestMediaRefusesStoredBytesThatDoNotMatchTheHash(t *testing.T) {
	f := newMediaFixture(t)
	main := f.image("main", "image/jpeg")
	stored := f.content[main.SHA256]
	stored.Bytes = []byte("tampered")
	f.content[main.SHA256] = stored
	_, err := f.provider.SyncProductMedia(context.Background(), f.request(main))
	wantConflict(t, err)
	if len(f.h.media.uploads) != 0 {
		t.Fatal("mismatched bytes uploaded")
	}
}

func TestMediaMissingFileStaysPending(t *testing.T) {
	f := newMediaFixture(t)
	main := f.image("main", "image/jpeg")
	delete(f.content, main.SHA256)
	if result := f.sync(t, f.request(main)); result.Pending != 1 || result.Created != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestMediaRefusesNonHTTPSUploadTarget(t *testing.T) {
	f := newMediaFixture(t)
	f.h.media.stagedScheme = "http"
	_, err := f.provider.SyncProductMedia(context.Background(), f.request(f.image("main", "image/jpeg")))
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorValidation || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v", err)
	}
	if len(f.h.media.uploads) != 0 {
		t.Fatal("bytes sent to a non-HTTPS target")
	}
}

func TestMediaUploadsAreBoundedPerRun(t *testing.T) {
	f := newMediaFixture(t)
	var images []commerce.MediaImage
	for i := 0; i < maxMediaCreatesPerSync+3; i++ {
		images = append(images, f.image(strings.Repeat("x", i+1), "image/jpeg"))
	}
	result := f.sync(t, f.request(images...))
	if result.Created != maxMediaCreatesPerSync || result.Pending != 3 {
		t.Fatalf("result = %+v", result)
	}
	result = f.sync(t, f.request(images...))
	if result.Created != 3 || result.Pending != 0 {
		t.Fatalf("second result = %+v", result)
	}
	if got := len(f.h.mediaOrder(f.product)); got != len(images) {
		t.Fatalf("attached = %d", got)
	}
}

func TestMediaMutationsSettle(t *testing.T) {
	for document, data := range map[string]string{
		docMediaStage:    `{"stagedUploadsCreate":{"stagedTargets":[],"userErrors":[]}}`,
		docMediaCreate:   `{"fileCreate":{"files":[{"id":"gid://shopify/MediaImage/1"}],"userErrors":[]}}`,
		docMediaAttach:   `{"fileUpdate":{"files":[],"userErrors":[]}}`,
		docMediaDelete:   `{"fileDelete":{"deletedFileIds":[],"userErrors":[]}}`,
		docMediaReorder:  `{"productReorderMedia":{"job":{"id":"gid://shopify/Job/1"},"userErrors":[]}}`,
		docMediaVariants: `{"productVariantsBulkUpdate":{"productVariants":[],"userErrors":[]}}`,
	} {
		if !mutationOutcomeComplete(document, []byte(data)) {
			t.Fatalf("%s did not settle", operationName(document))
		}
		if mutationOutcomeComplete(document, []byte(`{}`)) {
			t.Fatalf("%s settled without its result", operationName(document))
		}
	}
}
