package shopify

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

func moneyBag(amount string) map[string]any {
	return map[string]any{"shopMoney": map[string]any{"amount": amount, "currencyCode": "EGP"}}
}

// Phase 11 §92/§93: canonical status mapping table (documented).
func TestMapShopifyStatusTable(t *testing.T) {
	cases := []struct {
		financial, fulfillment string
		cancelled              bool
		want                   orders.CanonicalStatus
	}{
		{"PENDING", "UNFULFILLED", false, orders.StatusPending},
		{"AUTHORIZED", "UNFULFILLED", false, orders.StatusPending},
		{"PARTIALLY_PAID", "UNFULFILLED", false, orders.StatusPending},
		{"PAID", "UNFULFILLED", false, orders.StatusProcessing},
		{"PAID", "PARTIAL", false, orders.StatusProcessing},
		{"PAID", "FULFILLED", false, orders.StatusCompleted},
		{"PAID", "ON_HOLD", false, orders.StatusOnHold},
		{"PARTIALLY_REFUNDED", "UNFULFILLED", false, orders.StatusProcessing},
		{"REFUNDED", "FULFILLED", false, orders.StatusRefunded},
		{"VOIDED", "UNFULFILLED", false, orders.StatusFailed},
		{"EXPIRED", "UNFULFILLED", false, orders.StatusFailed},
		{"PAID", "FULFILLED", true, orders.StatusCancelled},
		{"WHATEVER", "SOMETHING", false, orders.StatusUnknown},
		{"", "", false, orders.StatusUnknown},
	}
	for _, tc := range cases {
		if got := MapShopifyStatus(tc.financial, tc.fulfillment, tc.cancelled); got != tc.want {
			t.Fatalf("MapShopifyStatus(%q,%q,%v) = %s, want %s",
				tc.financial, tc.fulfillment, tc.cancelled, got, tc.want)
		}
	}
}

func sampleOrder() map[string]any {
	return map[string]any{
		"id":                       "gid://shopify/Order/5231234567890",
		"legacyResourceId":         "5231234567890",
		"name":                     "#1001",
		"createdAt":                "2026-09-20T10:00:00Z",
		"updatedAt":                "2026-09-20T11:00:00Z",
		"processedAt":              "2026-09-20T10:05:00Z",
		"closedAt":                 "2026-09-21T09:00:00Z",
		"cancelledAt":              nil,
		"displayFinancialStatus":   "PAID",
		"displayFulfillmentStatus": "FULFILLED",
		"currencyCode":             "EGP",
		"taxesIncluded":            false,
		"paymentGatewayNames":      []any{"manual"},
		"email":                    "buyer@example.com",
		"phone":                    "+2010000000",
		"fullyPaid":                true,
		"totalPriceSet":            moneyBag("90071992547409.93"),
		"totalShippingPriceSet":    moneyBag("10.00"),
		"totalTaxSet":              moneyBag("5.00"),
		"totalDiscountsSet":        moneyBag("2.50"),
		"customer": map[string]any{
			"firstName": "Amal", "lastName": "Hassan",
			"email": "buyer@example.com", "phone": "+2010000000",
		},
		"billingAddress": map[string]any{
			"firstName": "Amal", "lastName": "Hassan", "company": "",
			"address1": "1 Nile St", "address2": "", "city": "Cairo",
			"provinceCode": "C", "zip": "11511", "countryCodeV2": "EG",
			"phone": "+2010000000",
		},
		"shippingAddress": map[string]any{
			"firstName": "Amal", "lastName": "Hassan", "company": "",
			"address1": "1 Nile St", "address2": "", "city": "Cairo",
			"provinceCode": "C", "zip": "11511", "countryCodeV2": "EG",
			"phone": "+2010000000",
		},
		"lineItems": map[string]any{"nodes": []any{map[string]any{
			"id":                 "gid://shopify/LineItem/13408043108543",
			"title":              "Tut Papyrus",
			"sku":                "PAP-001",
			"quantity":           float64(2),
			"originalTotalSet":   moneyBag("200.00"),
			"discountedTotalSet": moneyBag("180.00"),
			"taxLines": []any{map[string]any{
				"title": "VAT", "rate": "0.14", "priceSet": moneyBag("25.20"),
			}},
			"variant": map[string]any{"id": "gid://shopify/ProductVariant/40123456789012"},
			"product": map[string]any{"id": "gid://shopify/Product/7890123456789"},
		}}},
	}
}

// Phase 11 §168/§88/§89/§91: GetOrder normalization — canonical decimal
// identity, shop-money side only, exact money beyond 2^53, customer and
// address snapshots, line identity and product resolution fields.
func TestGetOrderNormalizesSnapshot(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	h.preloadOrder("5231234567890", sampleOrder())

	snapshot, err := provider.GetOrder(context.Background(), "5231234567890")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExternalOrderID != "5231234567890" || snapshot.ProviderKey != "shopify-main" {
		t.Fatalf("identity = %q/%q", snapshot.ProviderKey, snapshot.ExternalOrderID)
	}
	if snapshot.OrderNumber != "#1001" {
		t.Fatalf("order number = %q", snapshot.OrderNumber)
	}
	if snapshot.Canonical != orders.StatusCompleted {
		t.Fatalf("canonical = %s", snapshot.Canonical)
	}
	// >2^53 exact
	if snapshot.TotalMinor != 9007199254740993 {
		t.Fatalf("total = %d", snapshot.TotalMinor)
	}
	if snapshot.Currency != "EGP" || snapshot.ShippingMinor != 1000 ||
		snapshot.TotalTaxMinor != 500 || snapshot.DiscountMinor != 250 {
		t.Fatalf("money = %+v", snapshot)
	}
	if snapshot.PaidAt == nil || snapshot.CompletedAt == nil {
		t.Fatal("timestamps not normalized")
	}
	if snapshot.Customer.FirstName != "Amal" || snapshot.Customer.Email != "buyer@example.com" {
		t.Fatalf("customer = %+v", snapshot.Customer)
	}
	if snapshot.Billing.City != "Cairo" || snapshot.Shipping.Postcode != "11511" {
		t.Fatalf("addresses = %+v / %+v", snapshot.Billing, snapshot.Shipping)
	}
	if len(snapshot.Lines) != 1 {
		t.Fatalf("lines = %d", len(snapshot.Lines))
	}
	line := snapshot.Lines[0]
	if line.ExternalLineID != 13408043108543 {
		t.Fatalf("line id = %d", line.ExternalLineID)
	}
	if line.ExternalProductID != "7890123456789" {
		t.Fatalf("external product id = %q (parent Product identity)", line.ExternalProductID)
	}
	if line.VariationID != 0 {
		t.Fatal("shopify lines must not carry variation identity in the frozen domain")
	}
	if line.SKU != "PAP-001" || line.Quantity != 2 ||
		line.SubtotalMinor != 20000 || line.TotalMinor != 18000 || line.SubtotalTaxMinor != 2520 {
		t.Fatalf("line = %+v", line)
	}
	if snapshot.MappingComplete {
		t.Fatal("resolution runs at projection time; adapter must not claim mapping completeness")
	}
}

// Phase 11 §85: webhook and read paths share one canonical identity —
// GID and numeric payload forms normalize identically.
func TestWebhookOrderIDNormalizationMatchesGetOrder(t *testing.T) {
	fromGID, ok := WebhookOrderID([]byte(`{"admin_graphql_api_id":"gid://shopify/Order/5231234567890","id":5231234567890}`))
	if !ok || fromGID != "5231234567890" {
		t.Fatalf("gid normalization: %q %v", fromGID, ok)
	}
	fromNumber, ok := WebhookOrderID([]byte(`{"id":5231234567890}`))
	if !ok || fromNumber != fromGID {
		t.Fatalf("numeric normalization: %q %v", fromNumber, ok)
	}
	// Conflicting identities fail closed.
	if _, ok := WebhookOrderID([]byte(`{"admin_graphql_api_id":"gid://shopify/Order/1","id":2}`)); ok {
		t.Fatal("conflicting identities accepted")
	}
	if _, ok := WebhookOrderID([]byte(`{"admin_graphql_api_id":"gid://shopify/Product/5231234567890"}`)); ok {
		t.Fatal("non-Order gid accepted")
	}
	if _, ok := WebhookOrderID([]byte(`{"id":"drop table"}`)); ok {
		t.Fatal("garbage id accepted")
	}
}

// Phase 11 §122: a missing order proves absence; a transient failure
// must never read as deletion.
func TestGetOrderNotFoundAndTransient(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	_, err := provider.GetOrder(context.Background(), "4242")
	code, blocked := orders.IsBlocked(err)
	if !blocked || code != orders.CodeOrderNotFound {
		t.Fatalf("want ORDER_NOT_FOUND, got %v (%v)", err, blocked)
	}
	h.setFailure("MoonlightOrder", 500, `{"errors":[{"message":"boom"}]}`)
	_, err = provider.GetOrder(context.Background(), "4242")
	if code, blocked := orders.IsBlocked(err); blocked && code == orders.CodeOrderNotFound {
		t.Fatal("transient failure read as deletion")
	}
}

// Phase 11 §168: identity mismatch and invalid identity fail closed.
func TestGetOrderIdentityMismatch(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	raw := sampleOrder()
	raw["id"] = "gid://shopify/Order/9999999999999"
	h.preloadOrder("5231234567890", raw)
	_, err := provider.GetOrder(context.Background(), "5231234567890")
	code, blocked := orders.IsBlocked(err)
	if !blocked || code != orders.CodeOrderConflict {
		t.Fatalf("want ORDER_CONFLICT, got %v", err)
	}
	if _, err := provider.GetOrder(context.Background(), "not-a-number"); err == nil {
		t.Fatal("invalid external order id accepted")
	}
}

// Phase 11 §100: only generic order fields are normalized; nothing
// provider-opaque is retained on the snapshot.
func TestGetOrderLineWithDeletedProductStaysUnresolved(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	raw := sampleOrder()
	lines := raw["lineItems"].(map[string]any)["nodes"].([]any)
	line := lines[0].(map[string]any)
	line["product"] = nil // deleted product
	line["variant"] = nil
	h.preloadOrder("5231234567890", raw)
	snapshot, err := provider.GetOrder(context.Background(), "5231234567890")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Lines) != 1 {
		t.Fatal("deleted-product line dropped")
	}
	if snapshot.Lines[0].ExternalProductID != "" {
		t.Fatalf("deleted product resolved to %q", snapshot.Lines[0].ExternalProductID)
	}
}
