package shopify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

// Test harness: a deterministic fake Shopify GraphQL Admin server over
// TLS (Phase 11 §145). It emulates the documented contract only —
// GraphQL operation classification, variables, access-token
// verification, product/variant/metafield/publication/inventory/order
// state, compare-and-set, idempotency keys, rate limiting, error
// injection, connection drops, and redirects. It never prints secrets in
// failure output.
//
// The adapter under test must be built with harnessClient(harness).

type recordedRequest struct {
	Method    string
	URL       string
	Token     string
	Query     string
	Variables map[string]any
}

type fakeVariant struct {
	gid        string
	sku        string
	price      string
	title      string
	itemGID    string
	tracked    bool
	levels     map[string]int64 // location decimal -> available
	metafields map[string]string
}

type fakeProduct struct {
	gid        string
	status     string
	title      string
	desc       string
	metafields map[string]string // "namespace.key" -> value
	variants   []*fakeVariant
}

type fakeOrder struct {
	raw map[string]any
}

type shopifyHarness struct {
	bundleAttaches     []map[string]any
	frameComponentSets []map[string]any
	mu                 sync.Mutex
	server             *httptest.Server
	token              string
	secret             string
	requests           []recordedRequest

	products    map[string]*fakeProduct
	orders      map[string]*fakeOrder
	nextID      int64
	createCount int

	shopCurrency       string
	locationGID        string
	publicationGID     string
	published          map[string]bool     // product decimal -> published on configured publication
	publishedElsewhere map[string]bool     // product decimal -> published on an unrelated publication
	idemKeys           map[string][]string // operation -> keys in order
	idemApplied        map[string]bool     // key -> applied once (replay detection)
	casViolations      int

	// fault injection keyed by operation name substring
	failStatus map[string]int
	failBody   map[string]string
	dropAfter  map[string]bool
	redirectTo string // one-shot: next request is answered 302 here
	// concurrent hook fires before applying inventorySetQuantities
	beforeInventorySet func(h *shopifyHarness)
	// beforeOp fires before applying any operation (tests simulate a
	// concurrent operation completing mid-flight by mutating state here).
	beforeOp func(operation string, h *shopifyHarness)
	// media is the ADR-0056 file and product media state (media_harness_test.go).
	media *fakeMediaStore
}

func newHarness(t *testing.T) *shopifyHarness {
	return newHarnessWithHandler(t, nil)
}

func newHarnessWithHandler(t *testing.T, build func(*shopifyHarness) http.Handler) *shopifyHarness {
	t.Helper()
	h := &shopifyHarness{
		token:              "test-access-token-0123456789abcdef",
		secret:             "test-client-secret-0123456789abcdef",
		products:           map[string]*fakeProduct{},
		orders:             map[string]*fakeOrder{},
		nextID:             1000,
		shopCurrency:       "EGP",
		locationGID:        "gid://shopify/Location/7700000001",
		publicationGID:     "gid://shopify/Publication/7700000002",
		published:          map[string]bool{},
		publishedElsewhere: map[string]bool{},
		idemKeys:           map[string][]string{},
		idemApplied:        map[string]bool{},
		failStatus:         map[string]int{},
		failBody:           map[string]string{},
		dropAfter:          map[string]bool{},
	}
	var handler http.Handler = http.HandlerFunc(h.serve)
	if build != nil {
		handler = build(h)
	}
	h.media = newFakeMediaStore()
	h.server = httptest.NewTLSServer(handler)
	t.Cleanup(h.server.Close)
	return h
}

// harnessClient returns an *http.Client whose TLS trust accepts the
// harness and whose transport rewrites only requests addressed to the
// configured shop domain to the harness listener (test-only override).
// Requests to any other host pass through untouched, so redirect
// targeting is observable.
func harnessClient(t *testing.T, h *shopifyHarness) *http.Client {
	t.Helper()
	client := *h.server.Client()
	base := client.Transport
	client.Transport = &harnessTransport{h: h, base: base}
	return &client
}

type harnessTransport struct {
	h    *shopifyHarness
	base http.RoundTripper
}

func mustParseURL(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed
}

func (t *harnessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.EqualFold(req.URL.Hostname(), "test.myshopify.com") {
		clone := req.Clone(req.Context())
		target := t.h.server.URL
		clone.URL = mustParseURL(target + req.URL.RequestURI())
		clone.Host = mustParseURL(target).Host
		return t.base.RoundTrip(clone)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func testConfig() config.ShopifyConfig {
	return config.ShopifyConfig{
		Enabled:       true,
		ProviderKey:   "shopify-main",
		ShopDomain:    "test.myshopify.com",
		APIVersion:    "2026-10",
		AccessToken:   "test-access-token-0123456789abcdef",
		ClientSecret:  "test-client-secret-0123456789abcdef",
		Currency:      "EGP",
		LocationID:    "gid://shopify/Location/7700000001",
		PublicationID: "gid://shopify/Publication/7700000002",
		HTTPTimeout:   5 * time.Second,
	}
}

func (h *shopifyHarness) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()

	if strings.HasPrefix(r.URL.Path, stagedUploadPath) {
		h.serveStagedUpload(w, r, raw)
		return
	}

	if h.redirectTo != "" {
		// One-shot redirect response: the client must refuse to follow.
		target := h.redirectTo
		h.redirectTo = ""
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
		return
	}
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(raw, &body)
	h.requests = append(h.requests, recordedRequest{
		Method: r.Method, URL: r.URL.String(), Token: r.Header.Get("X-Shopify-Access-Token"),
		Query: body.Query, Variables: body.Variables,
	})
	if r.Header.Get("X-Shopify-Access-Token") != h.token {
		writeGQL(w, http.StatusUnauthorized, `{"errors":[{"message":"access denied"}]}`)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/graphql.json") || !strings.Contains(r.URL.Path, "/admin/api/") {
		writeGQL(w, http.StatusNotFound, `{"errors":[{"message":"not found"}]}`)
		return
	}

	if err := validateWireContract(body.Query, body.Variables); err != nil {
		writeGQL(w, 200, `{"errors":[{"message":"invalid GraphQL contract"}]}`)
		return
	}
	operation := operationName(body.Query)
	if status, ok := h.failStatus[operation]; ok {
		payload := h.failBody[operation]
		if payload == "" {
			payload = `{"errors":[{"message":"injected failure"}]}`
		}
		writeGQL(w, status, payload)
		return
	}

	var payload map[string]any
	// beforeOp fires before applying any operation; the test closure is
	// responsible for one-shot behaviour (it sees every operation name).
	if h.beforeOp != nil {
		h.beforeOp(operation, h)
	}
	switch {
	case strings.Contains(body.Query, "MoonlightMedia"):
		payload = h.opMedia(operation, body.Variables)
	case strings.Contains(body.Query, "MoonlightShopCurrency"):
		payload = map[string]any{"data": map[string]any{"shop": map[string]any{"currencyCode": h.shopCurrency}}}
	case strings.Contains(body.Query, "MoonlightVariantSet"):
		payload = h.opVariantSet(body.Variables)
	case strings.Contains(body.Query, "MoonlightProductCreate"):
		payload = h.opProductCreate(body.Variables)
	case strings.Contains(body.Query, "MoonlightProductUpdate"):
		payload = h.opProductUpdate(body.Variables)
	case strings.Contains(body.Query, "MoonlightManagedVariantUpdate"):
		payload = h.opVariantUpdate(body.Variables)
	case strings.Contains(body.Query, "MoonlightMetafieldsSet"):
		payload = h.opMetafieldsSet(body.Variables)
	case strings.Contains(body.Query, "MoonlightPublish"):
		payload = h.opPublish(body.Variables, true)
	case strings.Contains(body.Query, "MoonlightUnpublish"):
		payload = h.opPublish(body.Variables, false)
	case strings.Contains(body.Query, "MoonlightInventoryActivate"):
		payload = h.opInventoryActivate(body.Variables)
	case strings.Contains(body.Query, "MoonlightInventorySet"):
		payload = h.opInventorySet(body.Variables)
	case strings.Contains(body.Query, "MoonlightVariantsBySKU"):
		payload = h.opVariantsBySKU(body.Variables)
	case strings.Contains(body.Query, "MoonlightOrder"):
		payload = h.opOrder(body.Variables)
	case strings.Contains(body.Query, "MoonlightInventoryVariant"):
		var variant any
		for _, product := range h.products {
			if data, ok := h.opProductQuery(map[string]any{"id": product.gid})["data"].(map[string]any); ok {
				raw := data["product"].(map[string]any)
				for _, node := range raw["variants"].(map[string]any)["nodes"].([]any) {
					v := node.(map[string]any)
					if str(v["id"]) == str(body.Variables["id"]) {
						variant = v
					}
				}
			}
		}
		payload = map[string]any{"data": map[string]any{"productVariant": variant}}
	case strings.Contains(body.Query, "MoonlightVariantPrices"):
		payload = map[string]any{"data": map[string]any{"productVariantsBulkUpdate": map[string]any{
			"productVariants": []any{}, "userErrors": []any{},
		}}}
	case strings.Contains(body.Query, "MoonlightBundleUpdate"):
		h.bundleAttaches = append(h.bundleAttaches, body.Variables)
		payload = map[string]any{"data": map[string]any{"productBundleUpdate": map[string]any{
			"productBundleOperation": map[string]any{"id": "gid://shopify/ProductBundleOperation/1", "status": "CREATED"},
			"userErrors":             []any{},
		}}}
	case strings.Contains(body.Query, "MoonlightBundleOperation"):
		payload = map[string]any{"data": map[string]any{"productOperation": map[string]any{
			"id": "gid://shopify/ProductBundleOperation/1", "status": "COMPLETE",
			"product": map[string]any{"id": "gid://shopify/Product/9000000001"}, "userErrors": []any{},
		}}}
	case strings.Contains(body.Query, "MoonlightFrameComponentSet"):
		h.frameComponentSets = append(h.frameComponentSets, body.Variables)
		variants := []any{}
		if input, ok := body.Variables["input"].(map[string]any); ok {
			if raw, ok := input["variants"].([]any); ok {
				for index, entry := range raw {
					variants = append(variants, map[string]any{
						"id":   fmt.Sprintf("gid://shopify/ProductVariant/91000000%02d", index),
						"name": variantNameFrom(entry.(map[string]any)),
					})
				}
			}
		}
		payload = map[string]any{"data": map[string]any{"productSet": map[string]any{
			"product": map[string]any{
				"id":       "gid://shopify/Product/9000000000",
				"variants": map[string]any{"nodes": variants},
			},
			"userErrors": []any{},
		}}}
	case strings.Contains(body.Query, "MoonlightBundleVariants"):
		nodes := []any{}
		for _, set := range h.frameComponentSets {
			if input, ok := set["input"].(map[string]any); ok {
				if raw, ok := input["variants"].([]any); ok {
					for index, entry := range raw {
						nodes = append(nodes, map[string]any{
							"id":   fmt.Sprintf("gid://shopify/ProductVariant/92000000%02d", index),
							"name": variantNameFrom(entry.(map[string]any)),
						})
					}
				}
			}
		}
		payload = map[string]any{"data": map[string]any{"product": map[string]any{
			"id":       str(body.Variables["id"]),
			"variants": map[string]any{"nodes": nodes},
		}}}
	case strings.Contains(body.Query, "MoonlightBundleBase"):
		nodes := []any{map[string]any{"namespace": "moonlight", "key": "base_component_id", "value": "gid://shopify/Product/8800000000"}}
		payload = map[string]any{"data": map[string]any{"product": map[string]any{
			"id":         str(body.Variables["id"]),
			"metafields": map[string]any{"nodes": nodes},
		}}}
	case strings.Contains(body.Query, "MoonlightProduct"):
		payload = h.opProductQuery(body.Variables)
	default:
		payload = map[string]any{"errors": []any{map[string]any{"message": "unknown operation"}}}
	}
	if h.dropAfter[operation] {
		// Simulate the connection dying after the remote applied the
		// mutation: the caller never sees the response.
		panic(http.ErrAbortHandler)
	}
	encoded, _ := json.Marshal(payload)
	writeGQL(w, http.StatusOK, string(encoded))
}

func operationName(query string) string {
	for _, name := range []string{
		"MoonlightMediaProduct", "MoonlightMediaFiles", "MoonlightMediaStage", "MoonlightMediaCreate",
		"MoonlightMediaAttach", "MoonlightMediaDelete", "MoonlightMediaReorder", "MoonlightMediaVariants",
		"MoonlightShopCurrency", "MoonlightProductCreate", "MoonlightProductUpdate",
		"MoonlightManagedVariantUpdate", "MoonlightMetafieldsSet", "MoonlightPublish",
		"MoonlightUnpublish", "MoonlightInventoryActivate", "MoonlightInventorySet",
		"MoonlightVariantsBySKU", "MoonlightOrder", "MoonlightInventoryVariant",
		"MoonlightVariantSet", "MoonlightProduct",
	} {
		if strings.Contains(query, name) {
			return name
		}
	}
	// Phase 15 bundle surface.
	if strings.Contains(query, "MoonlightBundleCreate") {
		return "bundle_create"
	}
	if strings.Contains(query, "MoonlightVariantPrices") {
		return "MoonlightVariantPrices"
	}
	if strings.Contains(query, "MoonlightBundleUpdate") {
		return "bundle_update"
	}
	if strings.Contains(query, "MoonlightBundleOperation") {
		return "bundle_operation"
	}
	if strings.Contains(query, "MoonlightFrameComponentSet") {
		return "frame_component_set"
	}
	if strings.Contains(query, "MoonlightBundleVariants") {
		return "bundle_variants"
	}
	if strings.Contains(query, "MoonlightBundleBase") {
		return "bundle_base"
	}
	return "unknown"
}

func writeGQL(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Shopify-API-Version", "2026-10")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// ---- operation handlers ----

// ---- helpers for tests ----

func (h *shopifyHarness) allocateID() string {
	h.nextID++
	return strconv.FormatInt(h.nextID, 10)
}

// preloadOrder registers a raw order projection for GetOrder tests.
func (h *shopifyHarness) preloadOrder(decimalID string, raw map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.orders[decimalID] = &fakeOrder{raw: raw}
}

// quantityOf returns the managed variant's available quantity at the
// configured location: a product without a level there is unsellable
// (0); a missing product/variant reports -1.
func (h *shopifyHarness) quantityOf(productDecimal, locationGID string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	product := h.products[productDecimal]
	if product == nil || len(product.variants) == 0 {
		return -1
	}
	if quantity, ok := product.variants[0].levels[locationGID]; ok {
		return quantity
	}
	return 0
}

// productOf returns the fake product for assertions.
func (h *shopifyHarness) productOf(productDecimal string) *fakeProduct {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.products[productDecimal]
}

// variantQuantityOf returns one variant's available quantity at the
// configured location (Phase 17 per-variant stock assertions).
func (h *shopifyHarness) variantQuantityOf(productDecimal, variantGID, locationGID string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	product := h.products[productDecimal]
	if product == nil {
		return -1
	}
	for _, variant := range product.variants {
		if variant.gid == variantGID {
			if quantity, ok := variant.levels[locationGID]; ok {
				return quantity
			}
			return 0
		}
	}
	return -1
}

// seedProduct registers fake remote product state (ownership metadata
// included) for mapping/recovery fixtures.
func (h *shopifyHarness) seedProduct(product *fakeProduct) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if product.metafields == nil {
		product.metafields = map[string]string{}
	}
	id := strings.TrimPrefix(product.gid, "gid://shopify/Product/")
	h.products[id] = product
}

func (h *shopifyHarness) creations() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.createCount
}

func (h *shopifyHarness) recorded() []recordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedRequest(nil), h.requests...)
}

func (h *shopifyHarness) violations(productDecimal string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	product := h.products[productDecimal]
	found := []string{}
	if product == nil {
		return found
	}
	for key := range product.metafields {
		if strings.HasPrefix(key, "__violation") {
			found = append(found, key)
		}
	}
	return found
}

func (h *shopifyHarness) keysFor(operation string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.idemKeys[operation]...)
}

func (h *shopifyHarness) setFailure(operation string, status int, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failStatus[operation] = status
	h.failBody[operation] = body
}

func (h *shopifyHarness) clearFailure(operation string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.failStatus, operation)
	delete(h.failBody, operation)
}

func (h *shopifyHarness) dropOnce(operation string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropAfter[operation] = true
}

func (h *shopifyHarness) clearDrop(operation string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.dropAfter, operation)
}

func str(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func fixturePage(nodes []any, vars map[string]any) map[string]any {
	start := 0
	if cursor := str(vars["after"]); cursor != "" {
		start, _ = strconv.Atoi(cursor)
	}
	end := start + 50
	if end > len(nodes) {
		end = len(nodes)
	}
	if start > end {
		start = end
	}
	cursor := fmt.Sprint(end)
	return map[string]any{"nodes": nodes[start:end], "pageInfo": map[string]any{"hasNextPage": end < len(nodes), "endCursor": cursor}}
}

// variantNameFrom renders the "Style / Color" name from a frame
// component variant input (matches the adapter's stable convention).
func variantNameFrom(entry map[string]any) string {
	values, _ := entry["optionValues"].([]any)
	style, color := "", ""
	for _, raw := range values {
		value := raw.(map[string]any)
		if str(value["optionName"]) == "Frame Style" {
			style = str(value["name"])
		}
		if str(value["optionName"]) == "Frame Color" {
			color = str(value["name"])
		}
	}
	if style == "No Frame" {
		return "No Frame"
	}
	return style + " / " + color
}
