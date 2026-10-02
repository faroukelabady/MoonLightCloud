package app

// Phase 9-R1 F01 end-to-end HTTP regression: an authenticated mode=all
// dashboard Products request must return 200 (the normalized Product SQL
// previously failed with postgres 42883 and the handler returned 500).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
)

func TestDashboardModeAllHTTPReturns200(t *testing.T) {
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(t, url))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()

	p, err := a.Devices.Create(ctx, "mode-all-dev")
	if err != nil {
		t.Fatal(err)
	}
	eventID := "33333333-3333-7333-8333-333333333333"
	saleID := "11111111-1111-4111-8111-111111111111"
	productID := "aaaaaaaa-aaaa-4aaa-8aaa-111111111111"
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ($1, $2, 'sale.finalized.v1', '2026-09-20T10:00:00Z', now(), '{}', '\x00')`, eventID, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO sales_projection
			(sale_id, source_event_id, source_device_id, sale_number, channel,
			 occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar,
			 shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en,
			 currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		VALUES ($1, $2, $3, 'MLR-MA', 'STORE',
			'2026-09-20T10:00:00Z', '2026-09-20T10:05:00Z', 'a','b','c','d','e','f','g',
			'EGP', 65000, 0, 0, 65000, now())`, saleID, eventID, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO sale_lines_projection
			(sale_id, sale_item_id, position, product_id, sku, product_name,
			 quantity, unit_price_minor, unit_currency, line_total_minor, line_currency)
		VALUES ($1, '22222222-2222-4222-8222-222222222222', 0, $2, 'MODE-ALL', 'Mode All',
			1, 65000, 'EGP', 65000, 'EGP')`, saleID, productID); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(a.Handler)
	defer srv.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	loginBody, _ := json.Marshal(map[string]string{"username": "op", "password": "op-test-password"})
	res, err := client.Post(srv.URL+"/api/v1/dashboard/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login status %d", res.StatusCode)
	}

	req, _ := http.NewRequest("GET",
		srv.URL+"/api/v1/dashboard/products?period=custom&from_date=2026-09-20&to_date=2026-09-20&mode=all", nil)
	req.Header.Set("Origin", srv.URL)
	out, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	body, _ := io.ReadAll(out.Body)
	if out.StatusCode != http.StatusOK {
		t.Fatalf("authenticated mode=all status %d: %s", out.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "MODE-ALL") {
		t.Fatalf("mode=all body missing product: %s", string(body))
	}
}
