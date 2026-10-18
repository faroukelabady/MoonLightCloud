package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// Phase 17-R0 Cloud v3: frozen per-line variant snapshots, catalog label
// immutability, v1/v2 coexistence, cross-version arbitration, and
// idempotent replay. The snapshots are sourced ONLY from the event.

func (e *saleEnv) ingestV3(t *testing.T, eventID, payload string) isync.BatchResult {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v3","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, payload)
	res, err := e.syncSvc.Ingest(context.Background(), e.devID, e.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return res
}

// v3Fixture builds a v3 payload from the shared USD fixture: every line
// carries the v2 tag snapshots PLUS the frozen variant snapshot exactly
// as in the published wire example.
func v3Fixture(t *testing.T, saleID, variantSKU string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(taggedSaleFixture(t, nil)), &m); err != nil {
		t.Fatal(err)
	}
	if saleID != "" {
		m["sale_id"] = saleID
	}
	for _, entry := range m["lines"].([]any) {
		line := entry.(map[string]any)
		line["variant_id"] = "bbbbbbbb-0000-4000-8000-000000000001"
		line["variant_sku"] = variantSKU
		line["variant_attributes"] = []any{
			map[string]any{
				"definition_code": "color", "value_code": "blue",
				"name_ar": "أزرق", "name_en": "Blue",
				"definition_name_ar": "اللون", "definition_name_en": "Color",
			},
		}
		line["variant_price_egp_cents"] = 65000
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSaleV3ProjectsVariantSnapshots(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "33333333-3333-7333-8333-333333333301"
	res := env.ingestV3(t, eventID, v3Fixture(t, "", "NFT-BLU-TRD"))
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	var variantSKU, attrs string
	var egp, usd *int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT variant_sku, variant_attributes::text, variant_price_egp_cents, variant_price_usd_cents
		 FROM sale_lines_projection`).Scan(&variantSKU, &attrs, &egp, &usd); err != nil {
		t.Fatal(err)
	}
	if variantSKU != "NFT-BLU-TRD" {
		t.Fatalf("variant sku snapshot: %q", variantSKU)
	}
	if egp == nil || *egp != 65000 || usd != nil {
		t.Fatalf("price override snapshot: %v %v", egp, usd)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(attrs), &parsed); err != nil {
		t.Fatalf("attributes json: %v (%s)", err, attrs)
	}
	if len(parsed) != 1 || parsed[0]["definition_code"] != "color" ||
		parsed[0]["name_ar"] != "أزرق" || parsed[0]["definition_name_en"] != "Color" {
		t.Fatalf("attributes verbatim: %s", attrs)
	}
	// Tag snapshots are mandatory for the canonical Sale contract.
	if n := saleCount(t, env.pool, "sale_item_tag_snapshots"); n != 2 {
		t.Fatalf("tag snapshots: %d", n)
	}

	// Same-event replay is idempotent: row counts never grow.
	env.drain(t)
	if n := saleCount(t, env.pool, "sale_lines_projection"); n != 1 {
		t.Fatalf("replay duplicated lines: %d", n)
	}
	if n := saleCount(t, env.pool, "sale_item_tag_snapshots"); n != 2 {
		t.Fatalf("replay duplicated tags: %d", n)
	}
}

// TestSaleV3LabelRenameImmutability is the history law (00036): the
// stored snapshot and its reads come ONLY from the event. Renaming the
// catalog attribute labels and the variant SKU afterwards can never
// rewrite what was sold.
func TestSaleV3LabelRenameImmutability(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	eventID := "33333333-3333-7333-8333-333333333302"
	if res := env.ingestV3(t, eventID, v3Fixture(t, "", "NFT-BLU-TRD")); res.Events[0].Status != "accepted" {
		t.Fatalf("ingest: %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Current catalog state exists (projected variant + attribute labels).
	if _, err := env.pool.Exec(ctx, `INSERT INTO catalog_product_variants
		(variant_id, product_id, sku, is_active, deleted, position, combination_key,
		 variant_revision, catalog_revision, source_event_id, source_device_id,
		 source_payload_hash, source_received_at, store_id)
		VALUES ('bbbbbbbb-0000-4000-8000-000000000001','22222222-2222-4222-8222-222222222222',
		 'NFT-BLU-TRD', true, false, 0, 'color=blue', 1, 1,
		 $1, $2, '\x00', now(), (SELECT store_id FROM device_store_bindings WHERE device_id = $2))`, eventID, env.devID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO catalog_product_variant_attribute_values
		(variant_id, definition_code, value_code, name_ar, name_en, definition_name_ar, definition_name_en, position)
		VALUES ('bbbbbbbb-0000-4000-8000-000000000001','color','blue','أزرق','Blue','اللون','Color',0)`); err != nil {
		t.Fatal(err)
	}

	readHistory := func() (string, string, []report.VariantRow) {
		t.Helper()
		var sku, attrs string
		if err := env.pool.QueryRow(ctx,
			`SELECT variant_sku, variant_attributes::text FROM sale_lines_projection`).Scan(&sku, &attrs); err != nil {
			t.Fatal(err)
		}
		store := NewDevices(env.pool, 5*time.Second)
		rows, err := store.SalesByVariant(ctx, time.Now().AddDate(-1, 0, 0), time.Now().Add(24*time.Hour), "")
		if err != nil {
			t.Fatal(err)
		}
		return sku, attrs, rows
	}
	skuBefore, attrsBefore, rowsBefore := readHistory()
	if len(rowsBefore) != 1 || rowsBefore[0].VariantSKU == nil || *rowsBefore[0].VariantSKU != "NFT-BLU-TRD" {
		t.Fatalf("report before rename: %+v", rowsBefore)
	}
	if len(rowsBefore[0].VariantAttributes) != 1 || rowsBefore[0].VariantAttributes[0].NameAR != "أزرق" {
		t.Fatalf("report labels before rename: %+v", rowsBefore[0].VariantAttributes)
	}

	// Rename every catalog label and the variant SKU.
	if _, err := env.pool.Exec(ctx,
		`UPDATE catalog_product_variant_attribute_values
		 SET name_ar='أزرق جديد', name_en='Blue Renamed', definition_name_ar='اللون الجديد', definition_name_en='Color Renamed'
		 WHERE variant_id='bbbbbbbb-0000-4000-8000-000000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE catalog_product_variants SET sku='NFT-RENAMED' WHERE variant_id='bbbbbbbb-0000-4000-8000-000000000001'`); err != nil {
		t.Fatal(err)
	}

	skuAfter, attrsAfter, rowsAfter := readHistory()
	if skuAfter != skuBefore || attrsAfter != attrsBefore {
		t.Fatalf("catalog rename rewrote history:\n%s\n%s", attrsBefore, attrsAfter)
	}
	if len(rowsAfter) != 1 || rowsAfter[0].VariantSKU == nil || *rowsAfter[0].VariantSKU != "NFT-BLU-TRD" {
		t.Fatalf("report after rename: %+v", rowsAfter)
	}
	if len(rowsAfter[0].VariantAttributes) != 1 || rowsAfter[0].VariantAttributes[0].NameAR != "أزرق" ||
		rowsAfter[0].VariantAttributes[0].DefinitionNameAR != "اللون" {
		t.Fatalf("report labels after rename: %+v", rowsAfter[0].VariantAttributes)
	}
}

// TestSaleV3SameSaleArbitration: sale_id ownership is per event — the
// first event wins and a later event for the same sale blocks as a
// conflict, never merging.
func TestSaleV3SameSaleArbitration(t *testing.T) {
	env := openSaleEnv(t)
	saleID := "11111111-1111-4111-8111-000000000008"
	if res := env.ingestV3(t, "33333333-3333-7222-8222-222222222208", taggedSaleFixtureWithSaleID(t, saleID)); res.Events[0].Status != "accepted" {
		t.Fatalf("v2 ingest: %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "v2 projection")

	if res := env.ingestV3(t, "33333333-3333-7333-8333-333333333308", v3Fixture(t, saleID, "NFT-BLU-TRD")); res.Events[0].Status != "accepted" {
		t.Fatalf("v3 ingest: %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool {
		var status string
		if err := env.pool.QueryRow(context.Background(),
			`SELECT status FROM sync_event_processing WHERE event_id='33333333-3333-7333-8333-333333333308'`).Scan(&status); err != nil {
			return false
		}
		return status == "blocked"
	}, "v3 conflict")

	var code string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT COALESCE(last_error_code,'') FROM sync_event_processing WHERE event_id='33333333-3333-7333-8333-333333333308'`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != ErrSaleIDConflict {
		t.Fatalf("conflict code: %q", code)
	}
	// The winner's projection is untouched (no variant snapshot leak).
	var sku *string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT variant_sku FROM sale_lines_projection`).Scan(&sku); err != nil {
		t.Fatal(err)
	}
	if sku != nil {
		t.Fatal("loser must never write into the winner's history")
	}
}

// taggedSaleFixtureWithSaleID mirrors v2Fixture with a chosen sale identity.
func taggedSaleFixtureWithSaleID(t *testing.T, saleID string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(taggedSaleFixture(t, nil)), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestSaleV3StoreIsolation: v3 projections carry the trusted ingress
// Store context verbatim; a foreign-Store event for an existing sale is a
// scope conflict (history is immutable), and Store-scoped variant
// reporting never crosses ownership.
func TestSaleV3StoreIsolation(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	saleID := "11111111-1111-4111-8111-000000000031"
	f.ingest(t, f.devA, f.credA, "e0000131-0000-4000-8000-000000000031",
		sale.EventSaleFinalizedV3, v3Fixture(t, saleID, "NFT-BLU-TRD"))
	res := f.projectSaleV3(t, "e0000131-0000-4000-8000-000000000031")
	if res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("store A projection: %+v", res)
	}
	if got := f.rowStore(t, "sales_projection", "sale_id", saleID); got == nil || *got != scopeStoreA {
		t.Fatalf("sale store ownership: %v", got)
	}
	var sku string
	if err := f.pool.QueryRow(ctx,
		`SELECT variant_sku FROM sale_lines_projection WHERE sale_id=$1`, saleID).Scan(&sku); err != nil || sku != "NFT-BLU-TRD" {
		t.Fatalf("variant snapshot: %q %v", sku, err)
	}
	// Foreign-Store event for the same sale: scope conflict, no writes.
	f.ingest(t, f.devB, f.credB, "e0000131-0000-4000-8000-000000000032",
		sale.EventSaleFinalizedV3, v3Fixture(t, saleID, "FOREIGN-SKU"))
	second := f.projectSaleV3(t, "e0000131-0000-4000-8000-000000000032")
	if second.Outcome != sale.OutcomeBlocked {
		t.Fatalf("foreign-store sale must block: %+v", second)
	}
	if second.ErrorCode != ErrStoreScopeConflict && second.ErrorCode != ErrSaleIDConflict {
		t.Fatalf("conflict code: %q", second.ErrorCode)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT variant_sku FROM sale_lines_projection WHERE sale_id=$1`, saleID).Scan(&sku); err != nil || sku != "NFT-BLU-TRD" {
		t.Fatalf("history rewritten by foreign store: %q %v", sku, err)
	}
	// Store-scoped variant reporting stays inside ownership.
	devices := NewDevices(f.pool, 5*time.Second)
	rowsA, err := devices.SalesByVariantForStore(ctx, scopeStoreA,
		time.Now().AddDate(-1, 0, 0), time.Now().Add(24*time.Hour), "")
	if err != nil || len(rowsA) != 1 {
		t.Fatalf("store A variant rows: %+v %v", rowsA, err)
	}
	rowsB, err := devices.SalesByVariantForStore(ctx, scopeStoreB,
		time.Now().AddDate(-1, 0, 0), time.Now().Add(24*time.Hour), "")
	if err != nil || len(rowsB) != 0 {
		t.Fatalf("store B must see nothing: %+v %v", rowsB, err)
	}
}

// TestSaleV3ProjectsTypeSnapshot proves the R2 sale-time ProductType
// snapshot (00038): the frozen type id/code/labels project from the event
// only, then survive a type rename + product reassignment (history law).
func TestSaleV3ProjectsTypeSnapshot(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()

	var m map[string]any
	if err := json.Unmarshal([]byte(v3Fixture(t, "", "NFT-BLU-TRD")), &m); err != nil {
		t.Fatal(err)
	}
	for _, entry := range m["lines"].([]any) {
		line := entry.(map[string]any)
		line["product_type_id"] = "10000000-0000-4000-8000-000000000001"
		line["product_type_code"] = "papyrus"
		line["product_type_name_ar"] = "برديات"
		line["product_type_name_en"] = "Papyrus"
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	eventID := "33333333-3333-7333-8333-333333333311"
	if res := env.ingestV3(t, eventID, string(encoded)); res.Events[0].Status != "accepted" {
		t.Fatalf("ingest: %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	var typeID, typeCode, nameAR, nameEN string
	if err := env.pool.QueryRow(ctx,
		`SELECT product_type_id, product_type_code, product_type_name_ar, product_type_name_en
		 FROM sale_lines_projection`).Scan(&typeID, &typeCode, &nameAR, &nameEN); err != nil {
		t.Fatal(err)
	}
	if typeID != "10000000-0000-4000-8000-000000000001" || typeCode != "papyrus" || nameAR != "برديات" || nameEN != "Papyrus" {
		t.Fatalf("type snapshot: %q %q %q %q", typeID, typeCode, nameAR, nameEN)
	}

	// SalesByProductType serves the frozen bucket.
	store := NewDevices(env.pool, 5*time.Second)
	rows, err := store.SalesByProductType(ctx, time.Now().AddDate(-1, 0, 0), time.Now().Add(24*time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ProductTypeCode == nil || *rows[0].ProductTypeCode != "papyrus" {
		t.Fatalf("report rows: %+v", rows)
	}
}
