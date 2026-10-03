package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func r1OrderLines(count int) map[string]any {
	order := sampleOrder()
	template := order["lineItems"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	lines := []any{}
	for i := 0; i < count; i++ {
		line := map[string]any{}
		for k, v := range template {
			line[k] = v
		}
		line["id"] = fmt.Sprintf("gid://shopify/LineItem/%d", 10000+i)
		if i >= 50 {
			line["product"] = nil
			line["discountedTotalSet"] = moneyBag("90071992547409.93")
		}
		lines = append(lines, line)
	}
	order["lineItems"] = map[string]any{"nodes": lines}
	return order
}
func TestR1OrderPagination(t *testing.T) {
	for _, count := range []int{0, 50, 51, 151, 2001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h := newHarness(t)
			p := newTestProvider(t, h)
			h.preloadOrder("5231234567890", r1OrderLines(count))
			snapshot, err := p.GetOrder(context.Background(), "5231234567890")
			if count > maxConnectionNodes {
				if err == nil {
					t.Fatal("bound overflow accepted")
				}
				return
			}
			if err != nil || len(snapshot.Lines) != count {
				t.Fatalf("complete lines: %d %v", len(snapshot.Lines), err)
			}
			if count > 50 && (snapshot.Lines[50].TotalMinor != 9007199254740993 || snapshot.Lines[50].ExternalProductID != "") {
				t.Fatal("late-page exact money/deleted product lost")
			}
			if count > 0 && snapshot.Lines[count-1].ExternalLineID != int64(10000+count-1) {
				t.Fatal("line order changed")
			}
		})
	}
	for _, fault := range []string{"network", "repeat", "null-page", "missing-next", "empty-cursor", "identity", "duplicate-line"} {
		t.Run(fault, func(t *testing.T) {
			h := newHarness(t)
			p := newTestProvider(t, h)
			h.preloadOrder("5231234567890", r1OrderLines(51))
			h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var req gqlRequest
				_ = json.Unmarshal(raw, &req)
				if operationName(req.Query) == "MoonlightOrder" && req.Variables["after"] != nil {
					if fault == "network" {
						writeGQL(w, 503, `{"errors":[{"message":"temporary"}]}`)
						return
					}
					page := r1OrderLines(1)
					page["lineItems"] = fixturePage(page["lineItems"].(map[string]any)["nodes"].([]any), map[string]any{})
					connection := page["lineItems"].(map[string]any)
					switch fault {
					case "repeat":
						connection["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "50"}
					case "null-page":
						connection["pageInfo"] = nil
					case "missing-next":
						connection["pageInfo"] = map[string]any{}
					case "empty-cursor":
						connection["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": ""}
					case "identity":
						page["id"] = "gid://shopify/Order/1"
					case "duplicate-line":
					}
					encoded, _ := json.Marshal(map[string]any{"data": map[string]any{"order": page}})
					writeGQL(w, 200, string(encoded))
					return
				}
				h.serve(w, r)
			})
			if _, err := p.GetOrder(context.Background(), "5231234567890"); err == nil {
				t.Fatal("incomplete/malformed pagination accepted")
			}
		})
	}
}

func r1FuzzyProducts(h *shopifyHarness, count int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := 0; i < count; i++ {
		id := fmt.Sprint(900000 + i)
		h.products[id] = &fakeProduct{gid: "gid://shopify/Product/" + id, metafields: map[string]string{}, variants: []*fakeVariant{{gid: fmt.Sprintf("gid://shopify/ProductVariant/1000%03d", i), sku: fmt.Sprintf("PAP-001-FUZZY-%d", i)}}}
	}
}
func TestR1SKUCompleteLookup(t *testing.T) {
	for _, mode := range []string{"owned", "foreign", "multiple", "network", "repeat", "bound", "ambiguous-create"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			p := newTestProvider(t, h)
			owner := "prod-1"
			if mode == "foreign" {
				owner = "foreign"
			}
			if mode == "ambiguous-create" {
				h.mu.Lock()
				h.dropAfter["MoonlightProductCreate"] = true
				h.mu.Unlock()
			}
			first, err := p.UpsertProduct(context.Background(), upsertRequest(owner, "PAP-001", true))
			if mode == "ambiguous-create" {
				if err == nil {
					t.Fatal("ambiguous create succeeded")
				}
				h.mu.Lock()
				delete(h.dropAfter, "MoonlightProductCreate")
				h.mu.Unlock()
			} else if err != nil {
				t.Fatal(err)
			}
			r1FuzzyProducts(h, 60)
			if mode == "bound" {
				r1FuzzyProducts(h, 2001)
			}
			if mode == "multiple" {
				h.mu.Lock()
				original := h.products[first.ExternalProductID]
				other := *original
				other.gid = "gid://shopify/Product/800"
				v := *other.variants[0]
				v.gid = "gid://shopify/ProductVariant/800"
				other.variants = []*fakeVariant{&v}
				h.products["800"] = &other
				h.mu.Unlock()
			}
			if mode == "network" || mode == "repeat" {
				h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(bytes.NewReader(raw))
					var req gqlRequest
					_ = json.Unmarshal(raw, &req)
					if operationName(req.Query) == "MoonlightVariantsBySKU" && req.Variables["after"] != nil {
						if mode == "network" {
							writeGQL(w, 503, `{"errors":[{"message":"temporary"}]}`)
						} else {
							writeGQL(w, 200, `{"data":{"productVariants":{"nodes":[{"sku":"fuzzy"}],"pageInfo":{"hasNextPage":true,"endCursor":"50"}}}}`)
						}
						return
					}
					h.serve(w, r)
				})
			}
			recovered, err := p.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
			shouldSucceed := mode == "owned" || mode == "ambiguous-create"
			if (err == nil) != shouldSucceed {
				t.Fatalf("recovery %s: %v", mode, err)
			}
			if shouldSucceed && recovered.ExternalProductID != firstProductID(h) && mode == "ambiguous-create" { // fuzzy rows make map order irrelevant; require ownership instead below.
				h.mu.Lock()
				product := h.products[recovered.ExternalProductID]
				owned := product != nil && product.metafields[metafieldNamespace+"."+metafieldProductID] == "prod-1"
				h.mu.Unlock()
				if !owned {
					t.Fatal("wrong recovery identity")
				}
			}
			if mode == "owned" && recovered.ExternalProductID != first.ExternalProductID {
				t.Fatal("owned recovery remapped")
			}
			if h.creations() != 1 {
				t.Fatal("incomplete search created duplicate")
			}
			if mode == "network" {
				h.server.Config.Handler = http.HandlerFunc(h.serve)
				if _, err := p.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true)); err != nil {
					t.Fatal("retry after lookup failure", err)
				}
				if h.creations() != 1 {
					t.Fatal("lookup retry duplicated product")
				}
			}
		})
	}
}

func TestR1InventoryReplayAndRestart(t *testing.T) {
	h := newHarness(t)
	p := newTestProvider(t, h)
	created, err := p.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	inventory := inventoryRequest(created.ExternalProductID, "prod-1", 20, true)
	if err := p.SetInventory(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	update := staleRequest(created.ExternalProductID)
	update.OperationKey = "same-safe-zero-intent"
	if _, err := p.UpsertProduct(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if err := p.SetInventory(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	h.setFailure("MoonlightProductUpdate", 500, `{"errors":[{"message":"later update failed"}]}`)
	p = newTestProvider(t, h)
	if _, err := p.UpsertProduct(context.Background(), update); err == nil {
		t.Fatal("fault ignored")
	}
	if h.quantityOf(created.ExternalProductID, testLocationGID) != 0 {
		t.Fatal("safe-zero replay did not physically converge")
	}
	h.mu.Lock()
	delete(h.failStatus, "MoonlightProductUpdate")
	delete(h.failBody, "MoonlightProductUpdate")
	h.products[created.ExternalProductID].variants[0].levels[testLocationGID] = 30
	h.dropAfter["MoonlightInventorySet"] = true
	h.mu.Unlock()
	if err := p.SetInventory(context.Background(), inventory); err == nil {
		t.Fatal("ambiguous mutation not exposed")
	}
	h.mu.Lock()
	delete(h.dropAfter, "MoonlightInventorySet")
	h.mu.Unlock()
	p = newTestProvider(t, h)
	if err := p.SetInventory(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	if h.quantityOf(created.ExternalProductID, testLocationGID) != 20 {
		t.Fatal("post-interruption retry did not converge")
	}
}

func TestR1VersionAndRetryBounds(t *testing.T) {
	for _, version := range []string{"2026-02", "2026-13", "2024-01", "2027-01", "latest", "2026-10-01", "secret-value"} {
		cfg := testConfig()
		cfg.APIVersion = version
		if _, err := NewShopifyProvider(cfg, nil); err == nil || strings.Contains(err.Error(), version) {
			t.Fatal("invalid/value-bearing version validation")
		}
	}
	for _, served := range []string{"", "2026-07"} {
		h := newHarness(t)
		p := newTestProvider(t, h)
		h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Shopify-API-Version", served)
			io.WriteString(w, `{"data":{"shop":{"currencyCode":"EGP"}}}`)
		})
		if err := p.ensureShopCurrency(context.Background()); err == nil {
			t.Fatal("silent version fallback accepted")
		}
	}
	for _, input := range []string{"0", "10", "3600", "3601", "9223372036854775807", "-1", "bad", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)} {
		hint := parseRetryAfter(input)
		if hint < 0 || hint > maxRetryAfter {
			t.Fatal("retry hint out of bounds")
		}
		if input == "9223372036854775807" && hint != maxRetryAfter {
			t.Fatal("overflow not clamped")
		}
	}
	for _, code := range []string{"CHANGE_FROM_QUANTITY_STALE", "COMPARE_QUANTITY_STALE", "INVALID"} {
		h := newHarness(t)
		p := newTestProvider(t, h)
		made, err := p.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
		if err != nil {
			t.Fatal(err)
		}
		h.setFailure("MoonlightInventorySet", 200, fmt.Sprintf(`{"data":{"inventorySetQuantities":{"userErrors":[{"code":%q,"message":"bounded rejection"}]}}}`, code))
		err = p.SetInventory(context.Background(), inventoryRequest(made.ExternalProductID, "prod-1", 5, true))
		e, ok := err.(*commerce.ProviderError)
		if !ok || e.Retryable() != (code != "INVALID") {
			t.Fatal("documented CAS classification wrong", err)
		}
	}
}

func TestR1PhysicalRetryKeyStable(t *testing.T) {
	h := newHarness(t)
	p := newTestProvider(t, h)
	created, err := p.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	request := inventoryRequest(created.ExternalProductID, "prod-1", 20, true)
	if err := p.SetInventory(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.products[created.ExternalProductID].variants[0].levels[testLocationGID] = 30
	h.nextID++
	h.mu.Unlock()
	var keys []string
	var keysMu sync.Mutex
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var input gqlRequest
		_ = json.Unmarshal(raw, &input)
		if operationName(input.Query) == "MoonlightInventorySet" {
			quantities := input.Variables["input"].(map[string]any)["quantities"].([]any)
			if quantities[0].(map[string]any)["quantity"] == float64(20) {
				keysMu.Lock()
				keys = append(keys, input.Variables["idempotencyKey"].(string))
				first := len(keys) == 1
				keysMu.Unlock()
				if first {
					w.WriteHeader(503)
					return
				}
			}
		}
		h.serve(w, r)
	})
	if err := p.SetInventory(context.Background(), request); err == nil {
		t.Fatal("injected physical failure ignored")
	}
	p = newTestProvider(t, h)
	if err := p.SetInventory(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	keysMu.Lock()
	defer keysMu.Unlock()
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal("unchanged physical retry changed identity", keys)
	}
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 20 {
		t.Fatal("physical retry not converged", got)
	}
}
