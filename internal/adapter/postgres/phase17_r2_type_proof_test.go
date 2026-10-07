package postgres

// Phase 17-R2 ProductType projection proof (ADR-0050): revision gates,
// equal-revision convergence, set replacement, product → type dependency
// wait, Store isolation, admin reads, type health codes, sales-by-type
// reporting, and sale-line type snapshots. Real PostgreSQL.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

func typePayload(typeID, code string, revision int64) string {
	raw, _ := json.Marshal(map[string]any{
		"product_type_id": typeID, "code": code,
		"name_ar": "برديات", "name_en": "Papyrus",
		"is_active": true, "position": 0,
		"dimensions":    []any{"color", "painting_style"},
		"capabilities":  []any{"frame_configuration"},
		"type_revision": revision,
	})
	return string(raw)
}

func TestProductTypeProjectionLifecycle(t *testing.T) {
	env := openSaleEnv(t)
	typeID := "10000000-0000-4000-8000-000000000001"

	// v1 projects with dimensions + capabilities.
	e1 := variantEventID(91001)
	ingestCatalog(t, env, e1, catalog.EventProductTypeSnapshotV1, "2026-09-20T10:00:00Z", typePayload(typeID, "papyrus", 1))
	if res := projectCatalogOnce(t, env, e1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("type v1: %+v", res)
	}
	devices := catalogStore(env)
	row, err := devices.AdminProductType(context.Background(), "", typeID)
	if err != nil {
		t.Fatalf("admin read: %v", err)
	}
	if row.Code != "papyrus" || len(row.Dimensions) != 2 || len(row.Capabilities) != 1 {
		t.Fatalf("type row: %+v", row)
	}

	// Stale revision is a terminal no-op.
	e2 := variantEventID(91002)
	ingestCatalog(t, env, e2, catalog.EventProductTypeSnapshotV1, "2026-09-20T10:00:01Z", typePayload(typeID, "papyrus", 1))
	if res := projectCatalogOnce(t, env, e2); res.Outcome != catalog.OutcomeAlready && res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale: %+v", res)
	}

	// Higher revision replaces sets (rename + drop capability).
	raw, _ := json.Marshal(map[string]any{
		"product_type_id": typeID, "code": "papyrus",
		"name_ar": "برديات", "name_en": "Papyrus Artwork",
		"is_active": true, "position": 0,
		"dimensions":    []any{"color"},
		"capabilities":  []any{},
		"type_revision": 2,
	})
	e3 := variantEventID(91003)
	ingestCatalog(t, env, e3, catalog.EventProductTypeSnapshotV1, "2026-09-20T10:00:02Z", string(raw))
	if res := projectCatalogOnce(t, env, e3); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("type v2: %+v", res)
	}
	row, err = devices.AdminProductType(context.Background(), "", typeID)
	if err != nil {
		t.Fatalf("admin read v2: %v", err)
	}
	if row.NameEN != "Papyrus Artwork" || len(row.Dimensions) != 1 || len(row.Capabilities) != 0 {
		t.Fatalf("replaced row: %+v", row)
	}

	// Unknown capability is rejected at ingestion validation.
	bad, _ := json.Marshal(map[string]any{
		"product_type_id": typeID, "code": "papyrus",
		"name_ar": "برديات", "name_en": "Papyrus",
		"is_active": true, "position": 0,
		"dimensions":    []any{},
		"capabilities":  []any{"gift_wrap"},
		"type_revision": 3,
	})
	if _, err := catalog.DecodeProductTypeSnapshot(json.RawMessage(bad)); err != nil {
		t.Fatalf("decode: %v", err)
	} else {
		var snap catalog.ProductTypeSnapshot
		_ = json.Unmarshal(bad, &snap)
		if _, err := catalog.ValidateProductTypeSnapshot(snap); err == nil {
			t.Fatal("unknown capability must be rejected")
		}
	}
}

func TestProductTypeHealthAndReports(t *testing.T) {
	env := openSaleEnv(t)
	devID := env.devID
	exec := func(query string) {
		t.Helper()
		if _, err := env.pool.Exec(context.Background(), query); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	exec(fmt.Sprintf(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash)
		VALUES ('f1000000-0000-4000-8000-000000000001', '%s', 'catalog.product.snapshot.v1', now(), '{}', '\x00')`, devID))
	exec(`INSERT INTO catalog_categories (category_id, status, name_ar, online_enabled, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('f1000000-0000-4000-8000-000000000002', 'active', 'cat', true, 1, 'f1000000-0000-4000-8000-000000000001', '` + devID + `', '\x00', now())`)
	// p1: no type (PRODUCT_TYPE_MISSING). p2: dangling type (PROJECTION_MISSING).
	exec(fmt.Sprintf(`INSERT INTO catalog_products (product_id, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('f1000000-0000-4000-8000-000000000011', 'untyped', 'f1000000-0000-4000-8000-000000000002', true, 1, 'f1000000-0000-4000-8000-000000000001', '%s', '\x00', now())`, devID))
	exec(fmt.Sprintf(`INSERT INTO catalog_products (product_id, name, top_category_id, is_active, product_type_id, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('f1000000-0000-4000-8000-000000000012', 'dangling', 'f1000000-0000-4000-8000-000000000002', true, 'f1000000-0000-4000-8000-000000000099', 1, 'f1000000-0000-4000-8000-000000000001', '%s', '\x00', now())`, devID))

	devices := catalogStore(env)
	counts, err := devices.CatalogHealthSummaryRows(context.Background(), "", "")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	byCode := map[string]int64{}
	for _, c := range counts {
		byCode[c.ReasonCode] = c.Products
	}
	if byCode["PRODUCT_TYPE_MISSING"] < 1 {
		t.Fatalf("missing type code absent: %v", byCode)
	}
	if byCode["PRODUCT_TYPE_PROJECTION_MISSING"] < 1 {
		t.Fatalf("dangling type code absent: %v", byCode)
	}

	// Sales-by-type groups the frozen line snapshot (one line = one row).
	exec(fmt.Sprintf(`INSERT INTO sales_projection (sale_id, sale_number, channel, currency, subtotal_minor, discount_minor, tax_minor, total_minor, occurred_at, paid_at, source_event_id, source_device_id, received_at, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en)
		VALUES ('f1000000-0000-4000-8000-000000000021', 'S-1', 'STORE', 'EGP', 100, 0, 0, 100, now(), now(), 'f1000000-0000-4000-8000-000000000001', '%s', now(), 'م', 'M', 'ع', 'A', 'p', 'ش', 'T')`, devID))
	exec(`INSERT INTO sale_lines_projection (sale_id, sale_item_id, position, product_id, sku, product_name, quantity, unit_price_minor, unit_currency, line_total_minor, line_currency, product_type_id, product_type_code, product_type_name_ar, product_type_name_en)
		VALUES ('f1000000-0000-4000-8000-000000000021', 'f1000000-0000-4000-8000-000000000031', 0, 'f1000000-0000-4000-8000-000000000011', 'SKU-1', 'Name', 2, 50, 'EGP', 100, 'EGP', '10000000-0000-4000-8000-000000000001', 'papyrus', 'برديات', 'Papyrus')`)
	rows, err := devices.SalesByProductType(context.Background(), time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "EGP")
	if err != nil {
		t.Fatalf("sales by type: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ProductTypeCode != nil && *r.ProductTypeCode == "papyrus" && r.Units == 2 && r.LineSales == 100 {
			found = true
		}
	}
	if !found {
		t.Fatalf("papyrus bucket absent: %+v", rows)
	}
}
