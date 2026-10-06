package shopify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
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

func (h *shopifyHarness) opProductCreate(vars map[string]any) map[string]any {
	input, _ := vars["input"].(map[string]any)
	h.createCount++
	id := h.allocateID()
	product := &fakeProduct{
		gid:        "gid://shopify/Product/" + id,
		status:     str(input["status"]),
		title:      str(input["title"]),
		desc:       str(input["descriptionHtml"]),
		metafields: map[string]string{},
	}
	variantID := h.allocateID()
	variant := &fakeVariant{
		gid:        "gid://shopify/ProductVariant/" + variantID,
		itemGID:    "gid://shopify/InventoryItem/" + h.allocateID(),
		tracked:    true,
		levels:     map[string]int64{},
		metafields: map[string]string{},
	}
	if variants, ok := input["variants"].([]any); ok && len(variants) == 1 {
		if v, ok := variants[0].(map[string]any); ok {
			variant.sku = str(v["sku"])
			variant.price = str(v["price"])
			h.applyVariantMetafields(variant, v)
		}
	}
	if variants, ok := input["variants"].([]any); ok && len(variants) > 1 {
		// Phase 17 multi-variant create: one tracked variant per entry.
		product.variants = nil
		for _, raw := range variants {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			created := &fakeVariant{
				gid:        "gid://shopify/ProductVariant/" + h.allocateID(),
				itemGID:    "gid://shopify/InventoryItem/" + h.allocateID(),
				tracked:    true,
				levels:     map[string]int64{},
				metafields: map[string]string{},
				sku:        str(entry["sku"]),
				price:      str(entry["price"]),
			}
			h.applyVariantMetafields(created, entry)
			product.variants = append(product.variants, created)
		}
	}
	if metafields, ok := input["metafields"].([]any); ok {
		for _, raw := range metafields {
			if mf, ok := raw.(map[string]any); ok {
				product.metafields[str(mf["namespace"])+"."+str(mf["key"])] = str(mf["value"])
			}
		}
	}
	// create-time list fields: a non-empty tags/collections/media value
	// would be a contract violation for MoonLight creates; record it.
	if _, ok := input["tags"]; ok {
		product.metafields["__violation.tags"] = "sent"
	}
	if _, ok := input["collectionsToJoin"]; ok {
		product.metafields["__violation.collections"] = "sent"
	}
	if product.variants == nil {
		product.variants = []*fakeVariant{variant}
	}
	nodes := []any{}
	for _, created := range product.variants {
		nodes = append(nodes, map[string]any{"id": created.gid, "sku": created.sku})
	}
	h.products[id] = product
	return map[string]any{"data": map[string]any{"productSet": map[string]any{
		"product": map[string]any{
			"id":       product.gid,
			"variants": map[string]any{"nodes": nodes},
		},
		"userErrors": []any{},
	}}}
}

// opVariantSet converges one product's explicit variant set (Phase 17):
// entries with an id update the owned variant, entries without create a
// new tracked variant, and unlisted variants are removed (the same
// explicit-set semantics the frame component relies on).
func (h *shopifyHarness) opVariantSet(vars map[string]any) map[string]any {
	fail := func(message string) map[string]any {
		return map[string]any{"data": map[string]any{"productSet": map[string]any{
			"product": nil, "userErrors": []any{map[string]any{"message": message}}}}}
	}
	input, _ := vars["input"].(map[string]any)
	productID, err := ParseGID(str(input["id"]), ResourceProduct)
	if err != nil {
		return fail("product not found")
	}
	product := h.products[productID]
	if product == nil {
		return fail("product not found")
	}
	byID := map[string]*fakeVariant{}
	for _, existing := range product.variants {
		byID[strings.TrimPrefix(existing.gid, "gid://shopify/ProductVariant/")] = existing
	}
	entries, _ := input["variants"].([]any)
	kept := []*fakeVariant{}
	nodes := []any{}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		target := &fakeVariant{
			gid:        "gid://shopify/ProductVariant/" + h.allocateID(),
			itemGID:    "gid://shopify/InventoryItem/" + h.allocateID(),
			tracked:    true,
			levels:     map[string]int64{},
			metafields: map[string]string{},
		}
		if id := str(entry["id"]); id != "" {
			decimal, err := ParseGID(id, ResourceProductVariant)
			if err != nil {
				return fail("variant identity malformed")
			}
			existing := byID[decimal]
			if existing == nil {
				return fail("variant not found")
			}
			target = existing
		}
		if sku := str(entry["sku"]); sku != "" {
			target.sku = sku
		}
		if price := str(entry["price"]); price != "" {
			target.price = price
		}
		h.applyVariantMetafields(target, entry)
		kept = append(kept, target)
		nodes = append(nodes, map[string]any{"id": target.gid, "name": target.sku})
	}
	product.variants = kept
	options := []any{}
	if raw, ok := input["productOptions"].([]any); ok {
		for _, entry := range raw {
			option := entry.(map[string]any)
			options = append(options, map[string]any{
				"id":   "gid://shopify/ProductOption/" + h.allocateID(),
				"name": str(option["name"]),
			})
		}
	}
	return map[string]any{"data": map[string]any{"productSet": map[string]any{
		"product": map[string]any{
			"id":       product.gid,
			"options":  options,
			"variants": map[string]any{"nodes": nodes},
		},
		"userErrors": []any{},
	}}}
}

func (h *shopifyHarness) opProductUpdate(vars map[string]any) map[string]any {
	input, _ := vars["input"].(map[string]any)
	id, _ := ParseGID(str(input["id"]), ResourceProduct)
	product := h.products[id]
	if product == nil {
		return map[string]any{"data": map[string]any{"productUpdate": map[string]any{
			"product": nil, "userErrors": []any{map[string]any{"message": "product not found"}}}}}
	}
	product.title = str(input["title"])
	product.desc = str(input["descriptionHtml"])
	if status := str(input["status"]); status != "" {
		product.status = status
	}
	// Manual-field preservation guard: any managed-foreign field in the
	// update input is a contract violation.
	for _, forbidden := range []string{"tags", "vendor", "productType", "collectionsToJoin", "collectionsToLeave", "media", "seo", "category"} {
		if _, ok := input[forbidden]; ok {
			product.metafields["__violation."+forbidden] = "sent"
		}
	}
	return map[string]any{"data": map[string]any{"productUpdate": map[string]any{
		"product": map[string]any{"id": product.gid}, "userErrors": []any{}}}}
}

func (h *shopifyHarness) opVariantUpdate(vars map[string]any) map[string]any {
	productID, _ := ParseGID(str(vars["productId"]), ResourceProduct)
	product := h.products[productID]
	if product == nil {
		return map[string]any{"data": map[string]any{"productVariantsBulkUpdate": map[string]any{
			"productVariants": []any{}, "userErrors": []any{map[string]any{"message": "product not found"}}}}}
	}
	list, _ := vars["variants"].([]any)
	updated := []any{}
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		variantID, err := ParseGID(str(entry["id"]), ResourceProductVariant)
		if err != nil {
			continue
		}
		for _, variant := range product.variants {
			if strings.HasSuffix(variant.gid, variantID) {
				item, _ := entry["inventoryItem"].(map[string]any)
				variant.sku = str(item["sku"])
				variant.price = str(entry["price"])
				updated = append(updated, map[string]any{"id": variant.gid, "sku": variant.sku})
			}
		}
	}
	if len(list) != 1 {
		// Listing zero or many variants would delete/unmanaged-edit
		// manual variants: record the violation.
		product.metafields["__violation.variant_list"] = strconv.Itoa(len(list))
	}
	return map[string]any{"data": map[string]any{"productVariantsBulkUpdate": map[string]any{
		"productVariants": updated, "userErrors": []any{}}}}
}

func (h *shopifyHarness) opMetafieldsSet(vars map[string]any) map[string]any {
	list, _ := vars["metafields"].([]any)
	written := []any{}
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		ownerGID := str(entry["ownerId"])
		key := str(entry["namespace"]) + "." + str(entry["key"])
		value := str(entry["value"])
		productID, err := ParseGID(ownerGID, ResourceProduct)
		if err == nil {
			product := h.products[productID]
			if product == nil {
				continue
			}
			product.metafields[key] = value
			written = append(written, map[string]any{
				"namespace": str(entry["namespace"]), "key": str(entry["key"])})
			continue
		}
		// Phase 17: variant-level ownership/metafields (moonlight.variant_id
		// and the per-variant inventory revision fence).
		variantID, err := ParseGID(ownerGID, ResourceProductVariant)
		if err != nil {
			continue
		}
		if !h.applyVariantMetafieldGID(variantID, key, value) {
			continue
		}
		written = append(written, map[string]any{
			"namespace": str(entry["namespace"]), "key": str(entry["key"])})
	}
	return map[string]any{"data": map[string]any{"metafieldsSet": map[string]any{
		"metafields": written, "userErrors": []any{}}}}
}

// applyVariantMetafields copies create/update inline metafields onto one
// fake variant.
func (h *shopifyHarness) applyVariantMetafields(variant *fakeVariant, entry map[string]any) {
	list, _ := entry["metafields"].([]any)
	for _, raw := range list {
		mf, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if variant.metafields == nil {
			variant.metafields = map[string]string{}
		}
		variant.metafields[str(mf["namespace"])+"."+str(mf["key"])] = str(mf["value"])
	}
}

// applyVariantMetafieldGID writes one metafield on the variant with the
// given decimal identity.
func (h *shopifyHarness) applyVariantMetafieldGID(variantID, key, value string) bool {
	for _, product := range h.products {
		for _, variant := range product.variants {
			if strings.HasSuffix(variant.gid, variantID) {
				if variant.metafields == nil {
					variant.metafields = map[string]string{}
				}
				variant.metafields[key] = value
				return true
			}
		}
	}
	return false
}

func (h *shopifyHarness) opPublish(vars map[string]any, publish bool) map[string]any {
	productID, _ := ParseGID(str(vars["id"]), ResourceProduct)
	inputs, _ := vars["input"].([]any)
	publicationID := ""
	if len(inputs) == 1 {
		input, _ := inputs[0].(map[string]any)
		publicationID = str(input["publicationId"])
	}
	product := h.products[productID]
	if product == nil {
		return map[string]any{"data": map[string]any{"publishablePublish": map[string]any{
			"publishable": nil, "userErrors": []any{map[string]any{"message": "product not found"}}}}}
	}
	if publicationID == h.publicationGID {
		h.published[productID] = publish
	} else {
		h.publishedElsewhere[productID] = true
	}
	key := "publishablePublish"
	if !publish {
		key = "publishableUnpublish"
	}
	return map[string]any{"data": map[string]any{key: map[string]any{
		"publishable": map[string]any{"id": product.gid}, "userErrors": []any{}}}}
}

func (h *shopifyHarness) opInventoryActivate(vars map[string]any) map[string]any {
	key := str(vars["idempotencyKey"])
	h.idemKeys["activate"] = append(h.idemKeys["activate"], key)
	if h.idemApplied[key] {
		// Shopify idempotent replay: same key returns the original
		// success without re-applying the mutation.
		return map[string]any{"data": map[string]any{"inventoryActivate": map[string]any{
			"inventoryLevel": map[string]any{"id": "gid://shopify/InventoryLevel/replay"},
			"userErrors":     []any{}}}}
	}
	h.idemApplied[key] = true
	itemID, _ := ParseGID(str(vars["inventoryItemId"]), ResourceInventoryItem)
	locationID := str(vars["locationId"])
	for _, product := range h.products {
		for _, variant := range product.variants {
			if strings.HasSuffix(variant.itemGID, itemID) {
				if _, ok := variant.levels[locationID]; !ok {
					variant.levels[locationID] = 0
				}
				return map[string]any{"data": map[string]any{"inventoryActivate": map[string]any{
					"inventoryLevel": map[string]any{"id": "gid://shopify/InventoryLevel/" + h.allocateID()},
					"userErrors":     []any{}}}}
			}
		}
	}
	return map[string]any{"data": map[string]any{"inventoryActivate": map[string]any{
		"inventoryLevel": nil, "userErrors": []any{map[string]any{"message": "item not found"}}}}}
}

func (h *shopifyHarness) opInventorySet(vars map[string]any) map[string]any {
	key := str(vars["idempotencyKey"])
	h.idemKeys["set"] = append(h.idemKeys["set"], key)
	if h.idemApplied[key] {
		return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
			"inventoryAdjustmentGroup": map[string]any{"id": "gid://shopify/InventoryAdjustmentGroup/replay"},
			"userErrors":               []any{}}}}
	}
	h.idemApplied[key] = true
	if h.beforeInventorySet != nil {
		h.beforeInventorySet(h)
		h.beforeInventorySet = nil
	}
	input, _ := vars["input"].(map[string]any)
	if str(input["name"]) != "available" {
		return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
			"inventoryAdjustmentGroup": nil,
			"userErrors":               []any{map[string]any{"code": "INVALID", "message": "unsupported name"}}}}}
	}
	list, _ := input["quantities"].([]any)
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		itemID, err := ParseGID(str(entry["inventoryItemId"]), ResourceInventoryItem)
		if err != nil {
			continue
		}
		locationID := str(entry["locationId"])
		quantity := int64(0)
		switch v := entry["quantity"].(type) {
		case float64:
			quantity = int64(v)
		case json.Number:
			quantity, _ = strconv.ParseInt(v.String(), 10, 64)
		}
		for _, product := range h.products {
			for _, variant := range product.variants {
				if !strings.HasSuffix(variant.itemGID, itemID) {
					continue
				}
				current, active := variant.levels[locationID]
				if !active {
					return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
						"inventoryAdjustmentGroup": nil,
						"userErrors":               []any{map[string]any{"code": "INVALID", "message": "not activated"}}}}}
				}
				changeRaw, present := entry["changeFromQuantity"]
				if present && changeRaw != nil {
					changeFrom := int64(0)
					switch v := changeRaw.(type) {
					case float64:
						changeFrom = int64(v)
					case json.Number:
						changeFrom, _ = strconv.ParseInt(v.String(), 10, 64)
					}
					if changeFrom != current {
						h.casViolations++
						return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
							"inventoryAdjustmentGroup": nil,
							"userErrors": []any{map[string]any{
								"code": "CONFLICT", "message": "compare quantity mismatch"}}}}}
					}
				}
				variant.levels[locationID] = quantity
				return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
					"inventoryAdjustmentGroup": map[string]any{"id": "gid://shopify/InventoryAdjustmentGroup/" + h.allocateID()},
					"userErrors":               []any{}}}}
			}
		}
	}
	return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
		"inventoryAdjustmentGroup": nil,
		"userErrors":               []any{map[string]any{"code": "NOT_FOUND", "message": "item not found"}}}}}
}

func (h *shopifyHarness) opVariantsBySKU(vars map[string]any) map[string]any {
	query := str(vars["query"])
	wanted := strings.TrimPrefix(query, "sku:")
	nodes := []any{}
	for _, product := range h.products {
		for _, variant := range product.variants {
			// Search semantics are approximate: prefix matches are
			// returned too, so exact equality filtering is mandatory.
			if strings.HasPrefix(variant.sku, wanted) || strings.HasPrefix(wanted, variant.sku) {
				nodes = append(nodes, map[string]any{
					"id":              variant.gid,
					"sku":             variant.sku,
					"selectedOptions": []any{map[string]any{"name": "Title", "value": "Default Title"}},
					"product": map[string]any{
						"id":         product.gid,
						"status":     product.status,
						"options":    []any{map[string]any{"id": "gid://shopify/ProductOption/" + strings.TrimPrefix(product.gid, "gid://shopify/Product/"), "name": "Title"}},
						"metafields": map[string]any{"nodes": metafieldNodes(product.metafields)},
					},
				})
			}
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		return str(nodes[i].(map[string]any)["id"]) < str(nodes[j].(map[string]any)["id"])
	})
	return map[string]any{"data": map[string]any{"productVariants": fixturePage(nodes, vars)}}
}

func (h *shopifyHarness) opProductQuery(vars map[string]any) map[string]any {
	id, err := ParseGID(str(vars["id"]), ResourceProduct)
	if err != nil {
		return map[string]any{"data": map[string]any{"product": nil}}
	}
	product := h.products[id]
	if product == nil {
		return map[string]any{"data": map[string]any{"product": nil}}
	}
	variants := []any{}
	for _, variant := range product.variants {
		levels := []any{}
		for location, quantity := range variant.levels {
			levels = append(levels, map[string]any{
				"location":   map[string]any{"id": location},
				"updatedAt":  time.Unix(h.nextID, 0).UTC().Format(time.RFC3339Nano),
				"quantities": []any{map[string]any{"name": "available", "quantity": quantity}},
			})
		}
		variants = append(variants, map[string]any{
			"id":              variant.gid,
			"sku":             variant.sku,
			"title":           variant.title,
			"selectedOptions": []any{map[string]any{"name": "Title", "value": "Default Title"}},
			"metafields":      map[string]any{"nodes": metafieldNodes(variant.metafields)},
			"inventoryItem": map[string]any{
				"id":              variant.itemGID,
				"tracked":         variant.tracked,
				"inventoryLevels": map[string]any{"nodes": levels},
			},
		})
	}
	return map[string]any{"data": map[string]any{"product": map[string]any{
		"id":         product.gid,
		"status":     product.status,
		"options":    []any{map[string]any{"id": "gid://shopify/ProductOption/" + strings.TrimPrefix(product.gid, "gid://shopify/Product/"), "name": "Title"}},
		"metafields": map[string]any{"nodes": metafieldNodes(product.metafields)},
		"variants":   map[string]any{"nodes": variants},
	}}}
}

func (h *shopifyHarness) opOrder(vars map[string]any) map[string]any {
	id, err := ParseGID(str(vars["id"]), ResourceOrder)
	if err != nil {
		return map[string]any{"data": map[string]any{"order": nil}}
	}
	order := h.orders[id]
	if order == nil {
		return map[string]any{"data": map[string]any{"order": nil}}
	}
	raw := map[string]any{}
	for key, value := range order.raw {
		raw[key] = value
	}
	connection, _ := raw["lineItems"].(map[string]any)
	nodes, _ := connection["nodes"].([]any)
	raw["lineItems"] = fixturePage(nodes, vars)
	if connection["pageInfo"] != nil {
		raw["lineItems"] = connection
	}
	return map[string]any{"data": map[string]any{"order": raw}}
}

func metafieldNodes(values map[string]string) []any {
	nodes := []any{}
	for compound, value := range values {
		if strings.HasPrefix(compound, "__violation") {
			continue
		}
		parts := strings.SplitN(compound, ".", 2)
		nodes = append(nodes, map[string]any{
			"namespace": parts[0], "key": parts[1], "value": value})
	}
	return nodes
}

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
