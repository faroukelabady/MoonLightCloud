package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// bigSale builds a valid dense EGP sale with n lines and long bilingual
// names, mirroring the desktop 50-line probe shape.
func bigSale(t *testing.T, n int, nameRepeat int) string {
	t.Helper()
	longName := strings.Repeat("توت عنخ آمون Tutankhamun ", nameRepeat)
	type money map[string]any
	lines := make([]any, 0, n)
	var subtotal int64
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("33333333-3333-4333-8333-33333333%04d", i)
		product := fmt.Sprintf("22222222-2222-4222-8222-22222222%04d", i)
		lines = append(lines, map[string]any{
			"sale_item_id": id, "product_id": product,
			"sku": "PAP-001-70x100", "product_name": longName,
			"width_cm": 70, "height_cm": 100, "quantity": 3,
			"unit_price": money{"amount_minor": 1300, "currency": "EGP"},
			"cost":       money{"amount_minor": 400, "currency": "EGP"},
			"line_total": money{"amount_minor": 3900, "currency": "EGP"},
			"classifications": map[string]any{
				"roots":         []any{map[string]any{"category_id": "00000000-0000-0000-0000-000000000102", "name_ar": "فرعوني", "name_en": "Pharaonic"}},
				"subcategories": []any{map[string]any{"category_id": "00000000-0000-0000-0000-000000000301", "name_ar": "قطط", "name_en": "Cats"}},
			},
		})
		subtotal += 3900
	}
	m := map[string]any{
		"sale_id": "99999999-9999-4999-8999-999999999999", "sale_number": "MLR-BIG",
		"channel": "STORE", "occurred_at": "2026-09-20T10:00:00Z", "paid_at": "2026-09-20T10:00:00Z",
		"shop": map[string]any{"name_ar": "م", "name_en": "S", "address_ar": "ا", "address_en": "A",
			"phone": "+201000000000", "receipt_footer_ar": "ش", "receipt_footer_en": "T"},
		"actor":    map[string]any{"cashier_id": "c1", "cashier_name": "N"},
		"currency": "EGP",
		"totals": map[string]any{
			"subtotal": money{"amount_minor": subtotal, "currency": "EGP"},
			"discount": money{"amount_minor": 0, "currency": "EGP"},
			"tax":      money{"amount_minor": 0, "currency": "EGP"},
			"total":    money{"amount_minor": subtotal, "currency": "EGP"},
		},
		"lines": lines,
		"payments": []any{map[string]any{"method": "cash",
			"amount":       money{"amount_minor": subtotal, "currency": "EGP"},
			"change_given": money{"amount_minor": 0, "currency": "EGP"}}},
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestLargeSaleAcceptedAndProjected(t *testing.T) {
	env := openSaleEnv(t)
	payload := bigSale(t, 80, 10)
	size := len([]byte(payload))
	t.Logf("80-line dense payload: %d bytes (old 64KiB=%d, new 256KiB=%d)", size, 64*1024, 256*1024)
	if size <= 64*1024 {
		t.Fatalf("fixture must exceed the old limit to prove the change, got %d", size)
	}
	if size >= 256*1024 {
		t.Fatalf("fixture must stay below the new limit, got %d", size)
	}
	env.ingest(t, "22222222-2222-7222-8222-222222222222", payload)
	env.drain(t)
	waitFor(t, 15*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "large sale")
	if n := saleCount(t, env.pool, "sale_lines_projection"); n != 80 {
		t.Fatalf("want 80 lines, got %d", n)
	}
	if n := saleCount(t, env.pool, "sale_line_classifications_projection"); n != 160 {
		t.Fatalf("want 160 classifications, got %d", n)
	}
}

func TestPayloadSizeBoundaries(t *testing.T) {
	env := openSaleEnv(t)
	// Just under the boundary: pad product_name to approach 256KiB.
	base := bigSale(t, 5, 1)
	_ = base
	// Boundary probe in bytes: craft payloads of exact sizes via padding.
	makePayload := func(total int) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(bigSale(t, 1, 1)), &m); err != nil {
			t.Fatal(err)
		}
		line := m["lines"].([]any)[0].(map[string]any)
		pad := total - len([]byte(bigSale(t, 1, 1)))
		if pad < 0 {
			t.Fatal("base already larger than target")
		}
		line["product_name"] = strings.Repeat("x", len(line["product_name"].(string))+pad)
		out, _ := json.Marshal(m)
		return string(out)
	}
	under := makePayload(256*1024 - 1000)
	if len([]byte(under)) >= 256*1024 {
		t.Fatal("probe setup error")
	}
	env.ingest(t, "22222222-2222-7222-8222-222222222222", under)
	over := makePayload(256*1024 + 1000)
	if len([]byte(over)) <= 256*1024 {
		t.Fatal("probe setup error")
	}
	_, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID,
		[]byte(fmt.Sprintf(`{"events":[{"event_id":"33333333-3333-7333-8333-333333333333","event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`, over)))
	if err == nil {
		t.Fatal("payload over 256KiB must be rejected in bytes")
	}
}
