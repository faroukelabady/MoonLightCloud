package commerce

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// ADR-0056 step 2: provider-neutral image selection and orchestration.

type mediaProvider struct {
	*FakeProvider
	requests []MediaSyncRequest
	result   MediaSyncResult
}

func (m *mediaProvider) SyncProductMedia(_ context.Context, req MediaSyncRequest) (MediaSyncResult, error) {
	m.requests = append(m.requests, req)
	return m.result, nil
}

type mediaMappings struct {
	*stubMappings
	media []MediaMapping
}

func (m *mediaMappings) ListProductMediaMappings(context.Context, ProviderKey, string) ([]MediaMapping, error) {
	return m.media, nil
}

func (m *mediaMappings) CreateProductMediaMapping(_ context.Context, _ ProviderKey, _ string, mapping MediaMapping) error {
	m.media = append(m.media, mapping)
	return nil
}

func (m *mediaMappings) DeleteProductMediaMapping(context.Context, ProviderKey, string, MediaMapping) error {
	return nil
}

type noContent struct{}

func (noContent) MediaContent(context.Context, string) (MediaContent, error) {
	return MediaContent{}, nil
}

func sha(c string) string { return strings.Repeat(c, 64) }

func image(variant string, position int, primary bool, hash string, available bool) ProductImage {
	entry := ProductImage{Position: position, IsPrimary: primary, SHA256: sha(hash), ContentType: "image/jpeg",
		ByteSize: 10, Width: 4, Height: 3, Available: available}
	if variant != "" {
		entry.VariantID = &variant
	}
	return entry
}

func TestDesiredMediaOrder(t *testing.T) {
	product := CommerceProduct{Variants: []CommerceVariant{{VariantID: "v-small"}, {VariantID: "v-large"}}}
	images := []ProductImage{
		image("v-large", 0, true, "d", true),
		image("", 1, false, "b", true),
		image("", 0, false, "c", true),
		image("", 2, true, "a", true),
		image("v-small", 0, false, "e", false), // not uploaded yet
		image("v-small", 1, true, "b", true),   // same file as the Product gallery
		image("v-large", 1, false, "f", false),
	}
	ordered, variantMain, missing := desiredMedia(product, images)
	var got []string
	for _, entry := range ordered {
		got = append(got, entry.SHA256[:1])
	}
	// Product gallery primary first then by position, then each Variant
	// gallery in Variant order; a file shown twice is sent once.
	if want := []string{"a", "c", "b", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if want := map[string]string{"v-small": sha("b"), "v-large": sha("d")}; !reflect.DeepEqual(variantMain, want) {
		t.Fatalf("variant images = %v", variantMain)
	}
	if missing != 2 {
		t.Fatalf("missing = %d", missing)
	}

	// One provider product per variant set only: a single variant has no
	// separate variant image.
	single := CommerceProduct{Variants: []CommerceVariant{{VariantID: "v-small"}}}
	if _, variantMain, _ := desiredMedia(single, images); len(variantMain) != 0 {
		t.Fatalf("single variant images = %v", variantMain)
	}
}

func mediaService(provider CommerceProvider, desired DesiredProduct, mappings *mediaMappings) *CommerceService {
	registry := NewRegistry()
	if err := registry.Register(provider.Key(), provider); err != nil {
		panic(err)
	}
	service := NewCommerceService(registry, mappings, &stubSource{states: map[string]DesiredProduct{desired.Product.ProductID: desired}}, nil)
	service.SetMediaContent(noContent{})
	return service
}

func TestSyncProductSendsImagesAfterStock(t *testing.T) {
	ctx := context.Background()
	desired := desiredState("prod-1", true, true, 3, true)
	desired.Product.Names = []LocalizedName{{Locale: "en", Name: "Lotus"}, {Locale: "ar", Name: "لوتس"}}
	desired.Media = []ProductImage{image("", 0, true, "a", true), image("", 1, false, "b", false)}
	provider := &mediaProvider{FakeProvider: NewFakeProvider("primary")}
	mappings := &mediaMappings{stubMappings: &stubMappings{rows: map[string]ProductMapping{}}}

	result, err := mediaService(provider, desired, mappings).SyncProduct(ctx, "primary", "prod-1")
	if err != nil {
		t.Fatal(err)
	}
	if !result.InventoryUpdated || len(provider.requests) != 1 {
		t.Fatalf("inventory %v, media calls %d", result.InventoryUpdated, len(provider.requests))
	}
	req := provider.requests[0]
	if len(req.Images) != 1 || req.Images[0].SHA256 != sha("a") || req.Alt != "لوتس" || req.ExternalProductID != result.ExternalProductID {
		t.Fatalf("request = %+v", req)
	}
	if req.Content == nil || req.Recorder == nil || result.MediaMissing != 1 {
		t.Fatalf("content/recorder/missing = %v %v %d", req.Content, req.Recorder, result.MediaMissing)
	}

	// Files still processing: stock is done, the queue comes back later.
	provider.result = MediaSyncResult{Created: 1, Pending: 1}
	_, err = mediaService(provider, desired, mappings).SyncProduct(ctx, "primary", "prod-1")
	providerErr, ok := err.(*ProviderError)
	if !ok || !providerErr.Retryable() || !strings.Contains(err.Error(), "MEDIA_PENDING") {
		t.Fatalf("pending err = %v", err)
	}
	if got := len(provider.Inventories()); got != 2 {
		t.Fatalf("inventory updates = %d", got)
	}
}

func TestSyncProductSkipsImagesWithoutWork(t *testing.T) {
	ctx := context.Background()
	desired := desiredState("prod-1", true, true, 3, true)
	provider := &mediaProvider{FakeProvider: NewFakeProvider("primary")}
	mappings := &mediaMappings{stubMappings: &stubMappings{rows: map[string]ProductMapping{}}}
	if _, err := mediaService(provider, desired, mappings).SyncProduct(ctx, "primary", "prod-1"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 0 {
		t.Fatal("product without images or recorded media called the provider")
	}

	// Recorded media with no images left: the provider removes it.
	mappings.media = []MediaMapping{{SHA256: sha("a"), ExternalProductID: "x", ExternalMediaID: "1"}}
	if _, err := mediaService(provider, desired, mappings).SyncProduct(ctx, "primary", "prod-1"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 || len(provider.requests[0].Images) != 0 {
		t.Fatalf("cleanup request = %+v", provider.requests)
	}

	// A provider without the capability never sees images.
	plain := NewFakeProvider("primary")
	desired.Media = []ProductImage{image("", 0, true, "a", true)}
	if _, err := mediaService(plain, desired, mappings).SyncProduct(ctx, "primary", "prod-1"); err != nil {
		t.Fatal(err)
	}
}
