package woocommerce

import (
	"context"
	"net/http"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// TestWooSetInventoryIdentityMatrix proves the 2xx success invariant:
// the response must name the requested product with a positive valid
// Woo ID. Missing, malformed, zero, negative, and overflow identities
// are Temporary (the remote may have applied stock, but the response
// proves nothing); a different valid identity is a Conflict.
func TestWooSetInventoryIdentityMatrix(t *testing.T) {
	cases := []struct {
		name      string
		body      any
		wantErr   bool
		wantKind  commerce.ErrorKind
		wantRetry bool
	}{
		{"valid same ID", map[string]any{"id": float64(500)}, false, "", false},
		// String-form canonicalization ("000500" → 500) lives in
		// parseWooID (see TestWooIDNormalization); a JSON string in the
		// numeric id field is malformed input.
		{"string ID malformed", map[string]any{"id": "000500"}, true, commerce.ErrorTemporary, true},
		{"valid different ID", map[string]any{"id": float64(501)}, true, commerce.ErrorConflict, false},
		{"missing ID", map[string]any{"stock_quantity": 7}, true, commerce.ErrorTemporary, true},
		{"null ID", map[string]any{"id": nil}, true, commerce.ErrorTemporary, true},
		{"malformed ID", map[string]any{"id": "abc"}, true, commerce.ErrorTemporary, true},
		{"zero ID", map[string]any{"id": float64(0)}, true, commerce.ErrorTemporary, true},
		{"negative ID", map[string]any{"id": float64(-5)}, true, commerce.ErrorTemporary, true},
		{"overflow ID", map[string]any{"id": "99999999999999999999999"}, true, commerce.ErrorTemporary, true},
		{"path ID", map[string]any{"id": "500/x"}, true, commerce.ErrorTemporary, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newWooHarness(t, testConsumerKey, testConsumerSec)
			harness.preload(500, map[string]any{
				"sku": "PAP-001",
				"meta_data": []any{
					map[string]any{"key": "_moonlight_product_id", "value": "mx-id-1"},
					map[string]any{"key": "_moonlight_provider_key", "value": testProviderKey},
				},
			})
			harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
				if record.Method == http.MethodPut {
					return http.StatusOK, tc.body, true
				}
				return 0, nil, false
			})
			provider := testProvider(t, harness)
			err := provider.SetInventory(context.Background(), testInventoryReq("mx-id-1", "500", 7, true))
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("success: %v", err)
				}
				return
			}
			var providerErr *commerce.ProviderError
			if !asProviderError(err, &providerErr) {
				t.Fatalf("classified: %v", err)
			}
			if providerErr.Kind != tc.wantKind || providerErr.Retryable() != tc.wantRetry {
				t.Fatalf("got %v retryable=%v, want %v retryable=%v (%v)",
					providerErr.Kind, providerErr.Retryable(), tc.wantKind, tc.wantRetry, err)
			}
		})
	}
}

// TestWooAmbiguousInventorySyncResult proves CommerceService cannot
// report InventoryUpdated=true on an ambiguous 2xx: the service-level
// flag stays false and the error is Temporary.
func TestWooAmbiguousInventorySyncResult(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(500, map[string]any{
		"sku": "PAP-001",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "amb-1"},
			map[string]any{"key": "_moonlight_provider_key", "value": testProviderKey},
		},
	})
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		if record.Method == http.MethodPut {
			if _, hasName := record.Body["name"]; !hasName {
				return http.StatusOK, map[string]any{"stock_quantity": 7}, true
			}
		}
		return 0, nil, false
	})
	ctx := context.Background()
	mappings := &stubMappingRepo{rows: map[string]commerce.ProductMapping{
		"website/amb-1": {ProviderKey: "website", ProductID: "amb-1", ExternalProductID: "500"},
	}}
	service, _ := recoveryService(harness, testProduct("amb-1"), 7, true, mappings)
	result, err := service.SyncProduct(ctx, "website", "amb-1")
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("temporary: %v", err)
	}
	if result.InventoryUpdated {
		t.Fatalf("InventoryUpdated must stay false: %+v", result)
	}
}
