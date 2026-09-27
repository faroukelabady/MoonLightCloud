package woocommerce

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

func asProviderError(err error, target **commerce.ProviderError) bool {
	return errors.As(err, target)
}

func assertField(t *testing.T, body map[string]any, key string, want any) {
	t.Helper()
	if body[key] != want {
		t.Fatalf("%s: %v want %v", key, body[key], want)
	}
}

func metaMap(t *testing.T, body map[string]any) map[string]string {
	t.Helper()
	items, ok := body["meta_data"].([]any)
	if !ok {
		t.Fatalf("meta_data array: %v", body["meta_data"])
	}
	out := map[string]string{}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("meta entry: %v", item)
		}
		key, _ := entry["key"].(string)
		value, _ := entry["value"].(string)
		out[key] = value
	}
	return out
}

// tlsSink is a second TLS origin that must never receive forwarded
// credentials during redirect tests.
type tlsSink struct {
	server *httptest.Server
	hits   *atomic.Int64
}

func newTLSSink(t *testing.T) *tlsSink {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return &tlsSink{server: server, hits: &hits}
}

const (
	testProviderKey = "website"
	testConsumerKey = "ck_test_woocommerce"
	testConsumerSec = "cs_test_woocommerce"
)

func testConfig(harness *wooHarness) config.WooCommerceConfig {
	return config.WooCommerceConfig{
		Enabled: true, ProviderKey: testProviderKey, BaseURL: harness.url(),
		ConsumerKey: testConsumerKey, ConsumerSecret: testConsumerSec,
		Currency: config.WooCurrencyEGP, DimensionUnit: config.WooDimensionCM,
		HTTPTimeout: 5 * time.Second,
	}
}

func testProvider(t *testing.T, harness *wooHarness) *WooCommerceProvider {
	t.Helper()
	provider, err := NewWooCommerceProvider(testConfig(harness), harness.server.Client())
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if provider.Key() != commerce.ProviderKey(testProviderKey) {
		t.Fatalf("key: %q", provider.Key())
	}
	return provider
}

func testProduct(productID string) commerce.CommerceProduct {
	width, height := 70, 100
	cost := int64(40000)
	return commerce.CommerceProduct{
		ProductID: productID, SKU: "PAP-001", IsActive: true, SellOnline: true,
		Names: []commerce.LocalizedName{
			{Locale: "ar", Name: "بردية توت عنخ آمون"},
			{Locale: "en", Name: "Tutankhamun Papyrus"},
		},
		Descriptions: map[string]string{"ar": "وصف عربي", "en": "English description"},
		Prices: []commerce.Money{
			{Currency: "EGP", AmountMinor: 65000, CostMinor: &cost},
			{Currency: "USD", AmountMinor: 1300},
		},
		WidthCM: &width, HeightCM: &height,
		TopCategory:     commerce.CategoryRef{ID: "cat-root", NameAR: "جذر"},
		Subcategories:   []commerce.CategoryRef{{ID: "cat-sub", NameAR: "فرعي"}},
		Tags:            []commerce.TagRef{{ID: "tag-1", Slug: "gold", NameEN: "Gold"}},
		CatalogRevision: 12, PolicyRevision: 4,
	}
}

func testUpsertReq(product commerce.CommerceProduct, published bool, existing *commerce.ProviderProductRef) commerce.ProductUpsertRequest {
	return commerce.ProductUpsertRequest{
		ProviderKey: testProviderKey, ProductID: product.ProductID, ExistingExternal: existing,
		Product: product, Published: published,
		CatalogRevision: product.CatalogRevision, PolicyRevision: product.PolicyRevision,
		OperationKey: commerce.ProductOperationKey(testProviderKey, product.ProductID,
			product.CatalogRevision, product.PolicyRevision, published),
	}
}

func testInventoryReq(productID, external string, quantity int64, ready bool) commerce.InventoryUpdateRequest {
	return commerce.InventoryUpdateRequest{
		ProviderKey: testProviderKey, ProductID: productID, ExternalProductID: external,
		AvailableQuantity: quantity, InventoryRevision: 27,
		CatalogRevision: 12, PolicyRevision: 4, Ready: ready,
		OperationKey: commerce.InventoryOperationKey(testProviderKey, productID, 12, 4, 27, quantity, true),
	}
}

func lastRequest(t *testing.T, harness *wooHarness) wooRecordedRequest {
	t.Helper()
	requests := harness.recorded()
	if len(requests) == 0 {
		t.Fatal("no recorded requests")
	}
	return requests[len(requests)-1]
}

func TestWooAuthContract(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testProduct("auth-product-1")
	if _, err := provider.UpsertProduct(context.Background(), testUpsertReq(product, true, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, request := range harness.recorded() {
		if !request.HasAuth || request.Username != testConsumerKey {
			t.Fatalf("basic auth identity: %+v", request)
		}
		if request.Query.Has("consumer_key") || request.Query.Has("consumer_secret") {
			t.Fatalf("credentials in query: %v", request.Query)
		}
	}
	// Wrong credentials are rejected as authentication failures.
	bad, err := NewWooCommerceProvider(config.WooCommerceConfig{
		Enabled: true, ProviderKey: testProviderKey, BaseURL: harness.url(),
		ConsumerKey: "ck_wrong", ConsumerSecret: testConsumerSec,
		Currency: config.WooCurrencyEGP, DimensionUnit: config.WooDimensionCM,
		HTTPTimeout: 5 * time.Second,
	}, harness.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = bad.UpsertProduct(context.Background(), testUpsertReq(product, true, nil))
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorAuthentication {
		t.Fatalf("wrong credentials: %v", err)
	}
}

func TestWooRedirectRefusesAuthForward(t *testing.T) {
	sink := newTLSSink(t)
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.setRedirect(sink.server.URL + "/redirected")
	provider := testProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(), testUpsertReq(testProduct("redir-1"), true, nil))
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("redirect must be a retryable refusal: %v", err)
	}
	if got := sink.hits.Load(); got != 0 {
		t.Fatalf("redirect target must receive nothing, got %d", got)
	}
}

func TestWooCreatePayloadExact(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testProduct("create-1")
	result, err := provider.UpsertProduct(context.Background(), testUpsertReq(product, true, nil))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.ExternalProductID != "500" {
		t.Fatalf("first woo id: %q", result.ExternalProductID)
	}
	requests := harness.recorded()
	if len(requests) != 2 {
		t.Fatalf("sku preflight + post: %d requests", len(requests))
	}
	if requests[0].Method != http.MethodGet || !strings.HasPrefix(requests[0].Path, "/wp-json/wc/v3/products") {
		t.Fatalf("preflight: %+v", requests[0])
	}
	if requests[0].Query.Get("sku") != "PAP-001" {
		t.Fatalf("sku query: %v", requests[0].Query)
	}
	post := requests[1]
	if post.Method != http.MethodPost {
		t.Fatalf("post: %+v", post)
	}
	body := post.Body
	assertField(t, body, "type", "simple")
	assertField(t, body, "status", "publish")
	assertField(t, body, "catalog_visibility", "visible")
	assertField(t, body, "name", "بردية توت عنخ آمون")
	assertField(t, body, "description", "وصف عربي")
	assertField(t, body, "sku", "PAP-001")
	assertField(t, body, "regular_price", "650.00")
	assertField(t, body, "manage_stock", true)
	assertField(t, body, "stock_quantity", float64(0))
	assertField(t, body, "stock_status", "outofstock")
	assertField(t, body, "backorders", "no")
	dimensions, ok := body["dimensions"].(map[string]any)
	if !ok || dimensions["width"] != "70" || dimensions["height"] != "100" {
		t.Fatalf("dimensions: %v", body["dimensions"])
	}
	if _, present := dimensions["length"]; present {
		t.Fatalf("no length without moonlight depth: %v", dimensions)
	}
	for _, forbidden := range []string{"categories", "tags", "images", "sale_price", "cost", "featured", "tax_class", "shipping_class"} {
		if _, present := body[forbidden]; present {
			t.Fatalf("unmanaged field %q sent", forbidden)
		}
	}
	meta := metaMap(t, body)
	if meta["_moonlight_product_id"] != "create-1" || meta["_moonlight_provider_key"] != testProviderKey {
		t.Fatalf("ownership metadata: %v", meta)
	}
	if meta["_moonlight_product_operation_key"] == "" || meta["_moonlight_catalog_revision"] != "12" || meta["_moonlight_policy_revision"] != "4" {
		t.Fatalf("revision metadata: %v", meta)
	}
	// Stored remote state carries safe-zero stock.
	stored := harness.productState(500)
	if stored["stock_quantity"] != float64(0) || stored["stock_status"] != "outofstock" {
		t.Fatalf("safe-zero remote: %v", stored)
	}
}

func TestWooUpdatePayloadNarrow(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(500, map[string]any{
		"name": "old", "sku": "PAP-001", "images": []any{"manual.jpg"},
		"categories": []any{float64(9)},
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "update-1"},
			map[string]any{"key": "_moonlight_provider_key", "value": testProviderKey},
			map[string]any{"key": "unrelated_plugin_key", "value": "keep-me"},
		},
	})
	provider := testProvider(t, harness)
	product := testProduct("update-1")
	result, err := provider.UpsertProduct(context.Background(),
		testUpsertReq(product, true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.ExternalProductID != "500" {
		t.Fatalf("id: %q", result.ExternalProductID)
	}
	requests := harness.recorded()
	if len(requests) != 2 || requests[0].Method != http.MethodGet || requests[1].Method != http.MethodPut {
		t.Fatalf("get + put: %+v", requests)
	}
	if !strings.HasSuffix(requests[1].Path, "/products/500") {
		t.Fatalf("put path: %s", requests[1].Path)
	}
	put := requests[1].Body
	assertField(t, put, "name", "بردية توت عنخ آمون")
	assertField(t, put, "regular_price", "650.00")
	for _, forbidden := range []string{"categories", "tags", "images"} {
		if _, present := put[forbidden]; present {
			t.Fatalf("unmanaged field %q sent", forbidden)
		}
	}
	// Unrelated metadata and manual fields survive the merge.
	stored := harness.productState(500)
	if stored["images"] == nil || stored["categories"] == nil {
		t.Fatalf("manual fields preserved: %v", stored)
	}
	meta := metaMap(t, map[string]any{"meta_data": stored["meta_data"]})
	if meta["unrelated_plugin_key"] != "keep-me" {
		t.Fatalf("unrelated metadata preserved: %v", meta)
	}
	if meta["_moonlight_product_operation_key"] == "" {
		t.Fatalf("operation key refreshed: %v", meta)
	}
}

func TestWooDisabledProductPayload(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(500, map[string]any{
		"sku": "PAP-001",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "disable-1"},
			map[string]any{"key": "_moonlight_provider_key", "value": testProviderKey},
		},
	})
	provider := testProvider(t, harness)
	product := testProduct("disable-1")
	_, err := provider.UpsertProduct(context.Background(),
		testUpsertReq(product, false, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	put := lastRequest(t, harness).Body
	assertField(t, put, "status", "draft")
	assertField(t, put, "catalog_visibility", "hidden")
	assertField(t, put, "stock_quantity", float64(0))
	assertField(t, put, "stock_status", "outofstock")
	stored := harness.productState(500)
	if stored["status"] != "draft" {
		t.Fatalf("remote disabled: %v", stored)
	}
}

func TestWooInventoryPayloadExact(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(500, map[string]any{"sku": "PAP-001"})
	provider := testProvider(t, harness)
	if err := provider.SetInventory(context.Background(), testInventoryReq("inv-1", "500", 7, true)); err != nil {
		t.Fatalf("inventory: %v", err)
	}
	request := lastRequest(t, harness)
	if request.Method != http.MethodPut || !strings.HasSuffix(request.Path, "/products/500") {
		t.Fatalf("put id: %+v", request)
	}
	body := request.Body
	assertField(t, body, "manage_stock", true)
	assertField(t, body, "stock_quantity", float64(7))
	assertField(t, body, "stock_status", "instock")
	assertField(t, body, "backorders", "no")
	for _, forbidden := range []string{"name", "description", "sku", "regular_price", "meta_data", "status"} {
		if _, present := body[forbidden]; present {
			t.Fatalf("metadata field %q in inventory update", forbidden)
		}
	}
	if err := provider.SetInventory(context.Background(), testInventoryReq("inv-1", "500", 0, true)); err != nil {
		t.Fatal(err)
	}
	if body := lastRequest(t, harness).Body; body["stock_status"] != "outofstock" {
		t.Fatalf("zero status: %v", body)
	}
	// Not-ready forces zero even with a nonzero caller quantity.
	if err := provider.SetInventory(context.Background(), testInventoryReq("inv-1", "500", 9, false)); err != nil {
		t.Fatal(err)
	}
	if body := lastRequest(t, harness).Body; body["stock_quantity"] != float64(0) || body["stock_status"] != "outofstock" {
		t.Fatalf("not-ready zero: %v", body)
	}
}

func TestWooCurrencyMatrix(t *testing.T) {
	cases := []struct {
		name      string
		currency  string
		prices    []commerce.Money
		want      string
		wantError bool
	}{
		{"egp exact", "EGP", []commerce.Money{{Currency: "EGP", AmountMinor: 65000}}, "650.00", false},
		{"usd exact", "USD", []commerce.Money{{Currency: "USD", AmountMinor: 1300}}, "13.00", false},
		{"zero", "EGP", []commerce.Money{{Currency: "EGP", AmountMinor: 0}}, "0.00", false},
		{"large exact", "EGP", []commerce.Money{{Currency: "EGP", AmountMinor: 9007199254740993}}, "90071992547409.93", false},
		{"missing configured", "EGP", []commerce.Money{{Currency: "USD", AmountMinor: 1300}}, "", true},
		{"no fallback usd", "USD", []commerce.Money{{Currency: "EGP", AmountMinor: 65000}}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newWooHarness(t, testConsumerKey, testConsumerSec)
			cfg := testConfig(harness)
			cfg.Currency = tc.currency
			provider, err := NewWooCommerceProvider(cfg, harness.server.Client())
			if err != nil {
				t.Fatal(err)
			}
			product := testProduct("cur-1")
			product.Prices = tc.prices
			_, err = provider.UpsertProduct(context.Background(), testUpsertReq(product, true, nil))
			if tc.wantError {
				var providerErr *commerce.ProviderError
				if !asProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorValidation {
					t.Fatalf("validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if price := lastRequest(t, harness).Body["regular_price"]; price != tc.want {
				t.Fatalf("price: %v want %s", price, tc.want)
			}
		})
	}
}

func TestWooLocaleMatrix(t *testing.T) {
	newNames := func(names ...commerce.LocalizedName) commerce.CommerceProduct {
		product := testProduct("loc-1")
		product.Names = names
		return product
	}
	cases := []struct {
		name    string
		product commerce.CommerceProduct
		want    string
		wantErr bool
	}{
		{"arabic primary", newNames(
			commerce.LocalizedName{Locale: "ar", Name: "بردية"},
			commerce.LocalizedName{Locale: "en", Name: "Papyrus"}), "بردية", false},
		{"english fallback", newNames(
			commerce.LocalizedName{Locale: "en", Name: "Papyrus"}), "Papyrus", false},
		{"both absent", newNames(), "", true},
		{"blank names", newNames(
			commerce.LocalizedName{Locale: "ar", Name: "  "}), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newWooHarness(t, testConsumerKey, testConsumerSec)
			provider := testProvider(t, harness)
			_, err := provider.UpsertProduct(context.Background(), testUpsertReq(tc.product, true, nil))
			if tc.wantErr {
				var providerErr *commerce.ProviderError
				if !asProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorValidation {
					t.Fatalf("validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name := lastRequest(t, harness).Body["name"]; name != tc.want {
				t.Fatalf("name: %v want %q", name, tc.want)
			}
		})
	}
}

func TestWooDimensions(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testProduct("dim-1")
	if _, err := provider.UpsertProduct(context.Background(), testUpsertReq(product, true, nil)); err != nil {
		t.Fatal(err)
	}
	dimensions, ok := lastRequest(t, harness).Body["dimensions"].(map[string]any)
	if !ok || dimensions["width"] != "70" || dimensions["height"] != "100" {
		t.Fatalf("dimensions: %v", lastRequest(t, harness).Body["dimensions"])
	}
	// Missing dimensions are omitted, never zero-invented.
	bare := testProduct("dim-2")
	bare.SKU = "PAP-002"
	bare.WidthCM, bare.HeightCM = nil, nil
	if _, err := provider.UpsertProduct(context.Background(), testUpsertReq(bare, true, nil)); err != nil {
		t.Fatal(err)
	}
	if _, present := lastRequest(t, harness).Body["dimensions"]; present {
		t.Fatal("absent dimensions must be omitted")
	}
}

func TestWooIDNormalization(t *testing.T) {
	for input, want := range map[string]int64{"794": 794, "000794": 794, " 794 ": 794} {
		if got, err := parseWooID(input); err != nil || got != want {
			t.Fatalf("%q: %d %v", input, got, err)
		}
	}
	for _, invalid := range []string{"", "0", "-3", "abc", "12x", "7.5", "99999999999999999999999"} {
		if _, err := parseWooID(invalid); err == nil {
			t.Fatalf("%q must fail", invalid)
		}
	}
	if canonicalExternalID(794) != "794" {
		t.Fatal("canonical decimal")
	}
}

func TestWooMoneyExact(t *testing.T) {
	for minor, want := range map[int64]string{
		0: "0.00", 1: "0.01", 99: "0.99", 100: "1.00",
		65000: "650.00", 9007199254740993: "90071992547409.93",
	} {
		if got, err := minorToDecimal(minor); err != nil || got != want {
			t.Fatalf("%d: %q %v", minor, got, err)
		}
	}
	if _, err := minorToDecimal(-1); err == nil {
		t.Fatal("negative must fail")
	}
}

func TestWooConstructorRejectsUnsafeURLs(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	base := testConfig(harness)
	for _, raw := range []string{
		"http://ck-review:sk-review@example.com",
		"https://ck-review:sk-review@example.com",
		"http://example.com/?consumer_secret=sk-review",
		"https://example.com/#sk-review",
		"http://%zz",
		"",
	} {
		t.Run(raw, func(t *testing.T) {
			cfg := base
			cfg.BaseURL = raw
			_, err := NewWooCommerceProvider(cfg, harness.server.Client())
			if err == nil {
				t.Fatalf("%q must fail", raw)
			}
			for _, forbidden := range []string{raw, "ck-review", "sk-review"} {
				if forbidden != "" && strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error echoes %q: %q", forbidden, err.Error())
				}
			}
		})
	}
}
