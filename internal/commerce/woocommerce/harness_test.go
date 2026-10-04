package woocommerce

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// wooRecordedRequest captures one harness call with safe fields only:
// auth presence and identity are recorded, never secret values in output.
type wooRecordedRequest struct {
	Method   string
	Path     string
	Query    url.Values
	HasAuth  bool
	Username string
	Body     map[string]any
	RawBody  string
}

// wooHarness is the deterministic HTTPS Woo contract server: it emulates
// only the Phase 6B endpoints (product list/get/create/update), records
// every call, keeps product state with Woo-style merge semantics, and
// supports injected errors. It is not WordPress.
type wooHarness struct {
	t               *testing.T
	server          *httptest.Server
	expectedKey     string
	expectedPass    string
	mu              sync.Mutex
	requests        []wooRecordedRequest
	products        map[int64]map[string]any
	nextID          int64
	wooOrders       map[int64]map[string]any
	nextOrderID     int64
	failWith        map[string]harnessFailure
	intercept       func(wooRecordedRequest) (status int, body any, handled bool)
	dropCreate      bool
	variations      map[int64]map[int64]map[string]any
	nextVariationID int64
	redirectTo      string
	redirectHits    int
}

type harnessFailure struct {
	status  int
	body    any
	headers map[string]string
}

// newWooHarness starts a TLS server expecting Basic Auth credentials.
// The adapter under test must be built with server.Client().
func newWooHarness(t *testing.T, key, secret string) *wooHarness {
	t.Helper()
	harness := &wooHarness{
		t: t, expectedKey: key, expectedPass: secret,
		products:    map[int64]map[string]any{},
		nextID:      500,
		wooOrders:   map[int64]map[string]any{},
		nextOrderID: 1000,
		failWith:    map[string]harnessFailure{},
	}
	harness.server = httptest.NewTLSServer(http.HandlerFunc(harness.serve))
	t.Cleanup(harness.server.Close)
	return harness
}

func (h *wooHarness) url() string { return h.server.URL }

// fail injects one exact failure for the next matching METHOD + path call.
func (h *wooHarness) fail(method, path string, status int, body any) {
	h.failHeaders(method, path, status, body, nil)
}

// failHeaders injects one exact failure with response headers.
func (h *wooHarness) failHeaders(method, path string, status int, body any, headers map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failWith[method+" "+path] = harnessFailure{status: status, body: body, headers: headers}
}

// setIntercept installs a request interceptor (race-safe).
func (h *wooHarness) setIntercept(fn func(wooRecordedRequest) (int, any, bool)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.intercept = fn
}

// setDropCreate arms ambiguous-POST simulation (race-safe).
func (h *wooHarness) setDropCreate(drop bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropCreate = drop
}

// setRedirect points every call at another origin (race-safe).
func (h *wooHarness) setRedirect(target string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.redirectTo = target
}

func (h *wooHarness) takeFailure(method, path string) (harnessFailure, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	failure, ok := h.failWith[method+" "+path]
	if ok {
		delete(h.failWith, method+" "+path)
	}
	return failure, ok
}

// preload inserts a stored Woo product with an explicit identity,
// modeling foreign or previously created remote state.
func (h *wooHarness) preload(id int64, body map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stored := map[string]any{}
	for key, value := range body {
		stored[key] = value
	}
	stored["id"] = float64(id)
	h.products[id] = stored
	if id >= h.nextID {
		h.nextID = id + 1
	}
}

func (h *wooHarness) recorded() []wooRecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wooRecordedRequest(nil), h.requests...)
}

// preloadOrder inserts a stored Woo order with an explicit identity.
func (h *wooHarness) preloadOrder(id int64, body map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stored := map[string]any{}
	for key, value := range body {
		stored[key] = value
	}
	stored["id"] = float64(id)
	h.wooOrders[id] = stored
	if id >= h.nextOrderID {
		h.nextOrderID = id + 1
	}
}

func (h *wooHarness) productState(id int64) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	stored, ok := h.products[id]
	if !ok {
		return nil
	}
	out := map[string]any{}
	for key, value := range stored {
		out[key] = value
	}
	return out
}

func (h *wooHarness) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *wooHarness) writeWooError(w http.ResponseWriter, status int, code, message string) {
	h.writeJSON(w, status, map[string]any{
		"code": code, "message": message, "data": map[string]any{"status": status},
	})
}

func (h *wooHarness) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var parsed map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &parsed)
	}
	username, password, hasAuth := r.BasicAuth()
	record := wooRecordedRequest{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
		HasAuth: hasAuth, Username: username, Body: parsed, RawBody: string(body),
	}
	h.mu.Lock()
	h.requests = append(h.requests, record)
	redirect := h.redirectTo
	h.mu.Unlock()

	if redirect != "" {
		if r.URL.Path != "/redirected" {
			h.mu.Lock()
			h.redirectHits++
			h.mu.Unlock()
			w.Header().Set("Location", redirect)
			w.WriteHeader(http.StatusFound)
			return
		}
	}
	// Auth gate: wrong credentials are rejected like Woo would.
	if !hasAuth || username != h.expectedKey || password != h.expectedPass {
		h.writeWooError(w, http.StatusUnauthorized, "woocommerce_rest_authentication_error", "Consumer key is missing.")
		return
	}
	if h.intercept != nil {
		if status, body, handled := h.intercept(record); handled {
			h.writeJSON(w, status, body)
			return
		}
	}
	if failure, ok := h.takeFailure(r.Method, r.URL.Path); ok {
		for key, value := range failure.headers {
			w.Header().Set(key, value)
		}
		h.writeJSON(w, failure.status, failure.body)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/wp-json/wc/v3/orders") {
		h.serveOrder(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/wp-json/wc/v3/products")
	// Phase 15: variable-product variations (product-level attributes;
	// per-variation stock is intentionally NOT modeled — real Woo pulls
	// variation stock from the parent when manage_stock is false).
	if match := variationRoute.FindStringSubmatch(rest); match != nil {
		h.serveVariations(w, r, parsed, match[1], match[2])
		return
	}
	switch {
	case r.Method == http.MethodGet && (rest == "" || rest == "/"):
		h.serveList(w, r)
	case r.Method == http.MethodPost && (rest == "" || rest == "/"):
		h.serveCreate(w, r, parsed)
	case strings.HasPrefix(rest, "/"):
		id, err := strconv.ParseInt(strings.Trim(rest, "/"), 10, 64)
		if err != nil || id <= 0 {
			h.writeWooError(w, http.StatusNotFound, "woocommerce_rest_term_invalid", "Resource doesn't exist.")
			return
		}
		switch r.Method {
		case http.MethodGet:
			h.serveGet(w, id)
		case http.MethodPut:
			h.serveUpdate(w, id, parsed)
		default:
			h.writeWooError(w, http.StatusBadRequest, "rest_no_route", "No route was found matching the URL and request method.")
		}
	default:
		h.writeWooError(w, http.StatusNotFound, "rest_no_route", "No route was found matching the URL and request method.")
	}
}

// serveOrder emulates GET /orders/{id} for Phase 6C reconciliation.
func (h *wooHarness) serveOrder(w http.ResponseWriter, r *http.Request) {
	if failure, ok := h.takeFailure(r.Method, r.URL.Path); ok {
		h.writeJSON(w, failure.status, failure.body)
		return
	}
	if r.Method != http.MethodGet {
		h.writeWooError(w, http.StatusBadRequest, "rest_no_route", "No route was found matching the URL and request method.")
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/wp-json/wc/v3/orders/"), 10, 64)
	if err != nil || id <= 0 {
		h.writeWooError(w, http.StatusNotFound, "woocommerce_rest_term_invalid", "Resource doesn't exist.")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	stored, ok := h.wooOrders[id]
	if !ok {
		h.writeWooError(w, http.StatusNotFound, "woocommerce_rest_term_invalid", "Resource doesn't exist.")
		return
	}
	h.writeJSON(w, http.StatusOK, stored)
}

func (h *wooHarness) serveList(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sku := r.URL.Query().Get("sku")
	var out []map[string]any
	for _, stored := range h.products {
		if sku != "" && stored["sku"] != sku {
			continue
		}
		out = append(out, stored)
	}
	if out == nil {
		out = []map[string]any{}
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *wooHarness) serveCreate(w http.ResponseWriter, r *http.Request, parsed map[string]any) {
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	stored := map[string]any{}
	for key, value := range parsed {
		stored[key] = value
	}
	stored["id"] = float64(id)
	h.products[id] = stored
	drop := h.dropCreate
	h.mu.Unlock()
	if drop {
		// Simulate an ambiguous transport failure: Woo created the
		// product, then the connection died before the response.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			h.writeWooError(w, http.StatusInternalServerError, "test_harness", "no hijack")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = connection.Close()
		return
	}
	h.writeJSON(w, http.StatusCreated, stored)
}

func (h *wooHarness) serveGet(w http.ResponseWriter, id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stored, ok := h.products[id]
	if !ok {
		h.writeWooError(w, http.StatusNotFound, "woocommerce_rest_term_invalid", "Resource doesn't exist.")
		return
	}
	h.writeJSON(w, http.StatusOK, stored)
}

// serveUpdate merges top-level keys and meta_data by key, modeling Woo
// PUT merge semantics: omitted unmanaged fields are preserved.
func (h *wooHarness) serveUpdate(w http.ResponseWriter, id int64, parsed map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stored, ok := h.products[id]
	if !ok {
		h.writeWooError(w, http.StatusNotFound, "woocommerce_rest_term_invalid", "Resource doesn't exist.")
		return
	}
	// Merge meta_data by key first so unrelated stored metadata survives;
	// all other top-level keys overlay like Woo PUT semantics.
	if incoming, ok := parsed["meta_data"].([]any); ok {
		merged := []any{}
		seen := map[string]bool{}
		for _, item := range incoming {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			key, _ := entry["key"].(string)
			seen[key] = true
			merged = append(merged, entry)
		}
		if existing, ok := stored["meta_data"].([]any); ok {
			for _, item := range existing {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				key, _ := entry["key"].(string)
				if !seen[key] {
					merged = append(merged, entry)
				}
			}
		}
		stored["meta_data"] = merged
	}
	for key, value := range parsed {
		if key == "id" || key == "meta_data" {
			continue
		}
		stored[key] = value
	}
	h.writeJSON(w, http.StatusOK, stored)
}

// variationRoute matches /{productID}/variations[/{variationID}].
var variationRoute = regexp.MustCompile(`^/(\d+)/variations(?:/(\d+))?$`)

// serveVariations is the Phase 15 fake for Woo variation CRUD. Product-
// level custom attributes are accepted as declared (matching real
// variable-product semantics); created variations inherit parent stock
// behavior by carrying no quantity.
func (h *wooHarness) serveVariations(w http.ResponseWriter, r *http.Request, parsed map[string]any, productID, variationID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pid, _ := strconv.ParseInt(productID, 10, 64)
	if h.variations == nil {
		h.variations = map[int64]map[int64]map[string]any{}
	}
	if h.variations[pid] == nil {
		h.variations[pid] = map[int64]map[string]any{}
	}
	switch {
	case r.Method == http.MethodGet && variationID == "":
		list := []map[string]any{}
		for _, variation := range h.variations[pid] {
			list = append(list, variation)
		}
		h.writeJSON(w, http.StatusOK, list)
	case r.Method == http.MethodPost && variationID == "":
		if h.dropCreate {
			h.mu.Unlock()
			h.server.CloseClientConnections()
			h.mu.Lock()
			return
		}
		h.nextVariationID++
		id := h.nextVariationID
		body := map[string]any{"id": id}
		for key, value := range parsed {
			body[key] = value
		}
		h.variations[pid][id] = body
		h.writeJSON(w, http.StatusCreated, body)
	case r.Method == http.MethodGet && variationID != "":
		vid, _ := strconv.ParseInt(variationID, 10, 64)
		if variation, ok := h.variations[pid][vid]; ok {
			h.writeJSON(w, http.StatusOK, variation)
			return
		}
		h.writeWooError(w, http.StatusNotFound, "woocommerce_rest_variation_invalid", "Resource doesn't exist.")
	case r.Method == http.MethodPut && variationID != "":
		vid, _ := strconv.ParseInt(variationID, 10, 64)
		variation, ok := h.variations[pid][vid]
		if !ok {
			h.writeWooError(w, http.StatusNotFound, "woocommerce_rest_variation_invalid", "Resource doesn't exist.")
			return
		}
		for key, value := range parsed {
			variation[key] = value
		}
		h.writeJSON(w, http.StatusOK, variation)
	default:
		h.writeWooError(w, http.StatusMethodNotAllowed, "woocommerce_rest_invalid_method", "Method not allowed.")
	}
}
