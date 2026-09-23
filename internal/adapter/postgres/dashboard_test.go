package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// dashEnv provisions a device and wires the dashboard service.
type dashEnv struct {
	*saleEnv
	dash dashboard.Service
	rep  report.Service
	loc  *time.Location
}

func openDashEnv(t *testing.T) *dashEnv {
	t.Helper()
	base := openSaleEnv(t)
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	store := NewDevices(base.pool, 5*time.Second)
	rep := report.NewService(store, clock.System{}, loc)
	return &dashEnv{
		saleEnv: base,
		dash:    dashboard.NewService(rep, store, store, clock.System{}),
		rep:     rep,
		loc:     loc,
	}
}

func dashReq(t *testing.T, env *dashEnv, kind, from, to string) report.Request {
	t.Helper()
	r, err := env.rep.ParseRequest(kind, from, to, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return r
}

// projectDashSale ingests a payload string and projects it synchronously.
func projectDashSale(t *testing.T, env *dashEnv, eventID, occurred, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":%q,"payload":%s}]}`,
		eventID, occurred, payload)
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadSaleEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err != nil {
		t.Fatalf("project: %v", err)
	}
}

// TestDashboardNormalizationProvesPerSaleFx seeds EGP + USD@R1 + USD@R2 and
// asserts All = EGP + A(R1) + B(R2), never latest-rate-for-all.
func TestDashboardNormalizationProvesPerSaleFx(t *testing.T) {
	env := openDashEnv(t)
	egp := fixture(t, "sale_egp.json")
	projectDashSale(t, env, "22222222-2222-7222-8222-222222222222", "2026-09-20T10:00:00Z", egp)
	// USD A @ 52.000000, total 1250 → 65000. USD B @ 48.500000, total 2500 → 121250.
	usdA := mutateUsd(t, fixture(t, "sale_usd.json"), "AAAAAAAA-1111-4111-8111-111111111111", "52.000000", 52000000, 1250)
	usdB := mutateUsd(t, fixture(t, "sale_usd.json"), "BBBBBBBB-2222-4222-8222-222222222222", "48.500000", 48500000, 2500)
	projectDashSale(t, env, "33333333-3333-7333-8333-333333333333", "2026-09-20T11:00:00Z", usdA)
	projectDashSale(t, env, "44444444-4444-7444-8444-444444444444", "2026-09-20T12:00:00Z", usdB)

	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	over, err := env.dash.Overview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// EGP native 200000 + 65000 + 121250 = 386250.
	if over.Normalized.NormalizedTotalMinor != "386250" {
		t.Fatalf("normalized: %+v", over.Normalized)
	}
	if over.Normalized.USDSaleCount != 2 || over.Normalized.Transactions != 3 || over.Normalized.Units != 4 {
		t.Fatalf("counts: %+v", over.Normalized)
	}
	// Native buckets untouched by normalization.
	if len(over.Summary.CurrencyTotals) != 2 {
		t.Fatalf("native buckets: %+v", over.Summary.CurrencyTotals)
	}
	// FX info: latest is B (48.5), multiple rates flagged, min/max exact.
	if !over.Fx.HasUSD || over.Fx.LatestRate == nil || *over.Fx.LatestRate != "48.500000" {
		t.Fatalf("fx latest: %+v", over.Fx)
	}
	if !over.Fx.MultipleRatesUsed || over.Fx.MinMicrorate == nil || *over.Fx.MinMicrorate != "48500000" ||
		over.Fx.MaxMicrorate == nil || *over.Fx.MaxMicrorate != "52000000" {
		t.Fatalf("fx range: %+v", over.Fx)
	}
}

// mutateUsd rewrites sale/total/payment/fx of the USD fixture.
func mutateUsd(t *testing.T, payload, saleID, rate string, micro, total int64) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	m["fx"] = map[string]any{"base": "USD", "quote": "EGP", "rate": rate, "rate_microrate": micro}
	tot := m["totals"].(map[string]any)
	tot["subtotal"].(map[string]any)["amount_minor"] = total
	tot["discount"].(map[string]any)["amount_minor"] = int64(0)
	tot["tax"].(map[string]any)["amount_minor"] = int64(0)
	tot["total"].(map[string]any)["amount_minor"] = total
	m["payments"] = []any{map[string]any{"method": "cash",
		"amount":       map[string]any{"amount_minor": total, "currency": "USD"},
		"change_given": map[string]any{"amount_minor": int64(0), "currency": "USD"}}}
	// Single line scaled to the total: qty 1 × total.
	lines := m["lines"].([]any)
	lines[0].(map[string]any)["quantity"] = 1
	lines[0].(map[string]any)["unit_price"].(map[string]any)["amount_minor"] = total
	lines[0].(map[string]any)["line_total"].(map[string]any)["amount_minor"] = total
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestDashboardBranchesGroupsShops verifies stable grouping by snapshot
// tuple + channel with exact counts, and no ONLINE fabrication.
func TestDashboardBranchesGroupsShops(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "22222222-2222-7222-8222-222222222222", "2026-09-20T10:00:00Z", fixture(t, "sale_egp.json"))
	projectDashSale(t, env, "33333333-3333-7333-8333-333333333333", "2026-09-20T11:00:00Z", fixture(t, "sale_egp.json"))
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	rows, err := env.dash.Branches(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("same shop snapshot groups once: %+v", rows)
	}
	b := rows[0]
	if b.Channel != "STORE" || b.Transactions != 1 || b.Units != 2 || b.SalesTotalMinor != "200000" {
		t.Fatalf("branch: %+v", b)
	}
	if b.ShopNameEN != "Papyrus Shop" || b.ShopPhone == "" {
		t.Fatalf("shop identity: %+v", b)
	}
}

// TestDashboardActivityBounded verifies latest-first bounded activity from
// real inbox/processing rows only.
func TestDashboardActivityBounded(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "22222222-2222-7222-8222-222222222222", "2026-09-20T10:00:00Z", fixture(t, "sale_egp.json"))
	items, err := env.dash.RecentActivity(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("activity must reflect real events")
	}
	kinds := map[string]bool{}
	var prev string
	for i, it := range items {
		kinds[it.Kind] = true
		if it.EventID == "" || it.EventType == "" || it.Timestamp == "" {
			t.Fatalf("activity fields: %+v", it)
		}
		if i > 0 && it.Timestamp > prev {
			t.Fatal("activity must be latest-first")
		}
		prev = it.Timestamp
		if it.DeviceName == nil || *it.DeviceName == "" {
			t.Fatalf("device attribution: %+v", it)
		}
	}
	if !kinds["accepted"] || !kinds["projected"] {
		t.Fatalf("kinds: %v", kinds)
	}
	if _, err := env.dash.RecentActivity(context.Background(), 0); err == nil {
		t.Fatal("limit 0 must fail")
	}
	if _, err := env.dash.RecentActivity(context.Background(), 101); err == nil {
		t.Fatal("limit 101 must fail")
	}
}

// TestDashboardLatestSalesFallback verifies newest-first finalized sales
// for the orders fallback (no lifecycle statuses invented).
func TestDashboardLatestSalesFallback(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "22222222-2222-7222-8222-222222222222", "2026-09-20T10:00:00Z", fixture(t, "sale_egp.json"))
	sales, err := env.dash.LatestSales(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sales) != 1 {
		t.Fatalf("sales: %+v", sales)
	}
	s := sales[0]
	if s.Channel != "STORE" || s.TotalMinor != "200000" || s.Currency != "EGP" {
		t.Fatalf("sale: %+v", s)
	}
	if _, err := env.dash.LatestSales(context.Background(), 0); err == nil {
		t.Fatal("limit 0 must fail")
	}
}

// TestDashboardMissingFxFailsLoudly proves a USD sale without an FX
// snapshot errors instead of silently dropping revenue. Such rows can only
// exist via legacy/corrupt paths (ingestion requires USD FX), so the row
// is seeded directly.
func TestDashboardMissingFxFailsLoudly(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ('22222222-2222-7222-8222-222222222222', $1, 'sale.finalized.v1', now(), now(), '{}', '\x00')`,
		env.devID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO sales_projection
			(sale_id, source_event_id, source_device_id, sale_number, channel,
			 occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar,
			 shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en,
			 currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		VALUES ('11111111-1111-4111-8111-111111111111',
			'22222222-2222-7222-8222-222222222222', $1,
			'MLR-X', 'STORE', now(), now(), 'a','b','c','d','e','f','g',
			'USD', 1000, 0, 0, 1000, now())`, env.devID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-25")
	if _, err := env.dash.Overview(ctx, req); err == nil {
		t.Fatal("USD without FX must fail loudly, never silently drop revenue")
	}
}
