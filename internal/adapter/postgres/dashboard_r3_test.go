package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// withSaleID rewrites the sale_id (and business timestamps, which the
// projector takes from the payload) inside a fixture payload so repeated
// projections neither collide into SALE_ID_CONFLICT nor share one day.
func withSaleID(t *testing.T, payload, saleID string) string {
	t.Helper()
	return withSale(t, payload, saleID, "", "")
}

func withSale(t *testing.T, payload, saleID, occurred, paid string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	if occurred != "" {
		m["occurred_at"] = occurred
	}
	if paid != "" {
		m["paid_at"] = paid
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestM01AtomicRoundingBoundary(t *testing.T) {
	env := openDashEnv(t)
	// Project two real sales on different Cairo dates for the invariant.
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", withSaleID(t, fixture(t, "sale_egp.json"), "11111111-1111-4111-8111-111111111111"))
	projectDashSale(t, env, "66666666-6666-4666-8666-666666666666", "2026-09-21T10:00:00Z", withSale(t, fixture(t, "sale_egp.json"), "22222222-2222-4222-8222-222222222222", "2026-09-21T10:00:00Z", "2026-09-21T10:05:00Z"))
	ctx := context.Background()
	// Rewrite to USD 49 @ 0.01 each: per-sale 0.49 -> 0 atomically.
	if _, err := env.pool.Exec(ctx, `UPDATE sales_projection SET currency='USD', subtotal_minor=49, discount_minor=0, tax_minor=0, total_minor=49, fx_base='USD', fx_quote='EGP', fx_rate='0.010000', fx_rate_microrate=10000`); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-21")
	over, err := env.dash.Overview(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// Atomic: 0 + 0 = 0. Aggregate-then-round would give round(0.98) = 1.
	if over.Normalized.NormalizedTotalMinor != "0" {
		t.Fatalf("atomic rounding: got %s want 0", over.Normalized.NormalizedTotalMinor)
	}
	// Invariant: overview == sum(daily) for the same scope.
	days, err := env.dash.Daily(ctx, req, "all")
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, d := range days.Days {
		var v int64
		if _, err := fmt.Sscanf(d.AmountMinor, "%d", &v); err != nil {
			t.Fatal(err)
		}
		sum += v
	}
	if fmt.Sprintf("%d", sum) != over.Normalized.NormalizedTotalMinor {
		t.Fatalf("overview %s != sum(daily) %d", over.Normalized.NormalizedTotalMinor, sum)
	}
	if len(days.Days) != 2 {
		t.Fatalf("two Cairo dates expected: %+v", days.Days)
	}
}

func TestM01HalfAwayBoundary(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", withSaleID(t, fixture(t, "sale_egp.json"), "11111111-1111-4111-8111-111111111111"))
	ctx := context.Background()
	// 150 minor USD @ 0.01 = 1.50 -> rounds to 2 (half away from zero).
	if _, err := env.pool.Exec(ctx, `UPDATE sales_projection SET currency='USD', subtotal_minor=150, total_minor=150, fx_base='USD', fx_quote='EGP', fx_rate='0.010000', fx_rate_microrate=10000`); err != nil {
		t.Fatal(err)
	}
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	over, err := env.dash.Overview(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if over.Normalized.NormalizedTotalMinor != "2" {
		t.Fatalf("half-away: got %s want 2", over.Normalized.NormalizedTotalMinor)
	}
}

func TestH03DailyModes(t *testing.T) {
	env := openDashEnv(t)
	egp := fixture(t, "sale_egp.json") // EGP total 200000
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", egp)
	usdA := mutateUsd(t, fixture(t, "sale_usd.json"), "AAAAAAAA-1111-4111-8111-111111111111", "52.000000", 52000000, 1250)
	usdB := mutateUsd(t, fixture(t, "sale_usd.json"), "BBBBBBBB-2222-4222-8222-222222222222", "48.500000", 48500000, 2500)
	projectDashSale(t, env, "66666666-6666-4666-8666-666666666666", "2026-09-20T11:00:00Z", usdA)
	projectDashSale(t, env, "77777777-7777-4777-8777-777777777777", "2026-09-20T12:00:00Z", usdB)
	ctx := context.Background()
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	all, err := env.dash.Daily(ctx, req, "all")
	if err != nil {
		t.Fatal(err)
	}
	egpMode, err := env.dash.Daily(ctx, req, "EGP")
	if err != nil {
		t.Fatal(err)
	}
	usdMode, err := env.dash.Daily(ctx, req, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if all.Mode != "all" || all.DisplayCurrency != "EGP" || !all.Normalized {
		t.Fatalf("all meta: %+v", all)
	}
	if egpMode.Mode != "EGP" || egpMode.DisplayCurrency != "EGP" || egpMode.Normalized {
		t.Fatalf("egp meta: %+v", egpMode)
	}
	if usdMode.Mode != "USD" || usdMode.DisplayCurrency != "USD" || usdMode.Normalized {
		t.Fatalf("usd meta: %+v", usdMode)
	}
	// All = 200000 + 65000 + 121250 = 386250; EGP-only 200000; USD-only 3750.
	if len(all.Days) != 1 || all.Days[0].AmountMinor != "386250" {
		t.Fatalf("all daily: %+v", all.Days)
	}
	if len(egpMode.Days) != 1 || egpMode.Days[0].AmountMinor != "200000" {
		t.Fatalf("egp daily: %+v", egpMode.Days)
	}
	if len(usdMode.Days) != 1 || usdMode.Days[0].AmountMinor != "3750" {
		t.Fatalf("usd daily: %+v", usdMode.Days)
	}
	if _, err := env.dash.Daily(ctx, req, "EUR"); err == nil {
		t.Fatal("bad mode must fail")
	}
}

func TestM02ServerAverages(t *testing.T) {
	env := openDashEnv(t)
	egp := fixture(t, "sale_egp.json") // 1 txn, total 200000
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", egp)
	usdA := mutateUsd(t, fixture(t, "sale_usd.json"), "AAAAAAAA-1111-4111-8111-111111111111", "52.000000", 52000000, 1250)
	usdB := mutateUsd(t, fixture(t, "sale_usd.json"), "BBBBBBBB-2222-4222-8222-222222222222", "52.000000", 52000000, 2500)
	projectDashSale(t, env, "66666666-6666-4666-8666-666666666666", "2026-09-20T11:00:00Z", usdA)
	projectDashSale(t, env, "77777777-7777-4777-8777-777777777777", "2026-09-20T12:00:00Z", usdB)
	ctx := context.Background()
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	over, err := env.dash.Overview(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// All: normalized 200000 + 65000 + 130000 = 395000 / 3 = 131666 trunc.
	if over.Averages.All.Transactions != 3 || over.Averages.All.AverageMinor != "131666" {
		t.Fatalf("all avg: %+v", over.Averages.All)
	}
	// EGP: 200000 / 1. USD native: (1250+2500)=3750 / 2 = 1875 trunc.
	if over.Averages.EGP.Transactions != 1 || over.Averages.EGP.AverageMinor != "200000" {
		t.Fatalf("egp avg: %+v", over.Averages.EGP)
	}
	if over.Averages.USD.Transactions != 2 || over.Averages.USD.AverageMinor != "1875" {
		t.Fatalf("usd avg: %+v", over.Averages.USD)
	}
	// R08: per-mode units follow the active currency (EGP fixture carries
	// 2 units, each USD fixture 1 unit): All 4, EGP 2, USD 2.
	if over.Averages.All.Units != 4 {
		t.Fatalf("all units: %+v", over.Averages.All)
	}
	if over.Averages.EGP.Units != 2 {
		t.Fatalf("egp units: %+v", over.Averages.EGP)
	}
	if over.Averages.USD.Units != 2 {
		t.Fatalf("usd units: %+v", over.Averages.USD)
	}
}

func TestM06NoRawDiagnostics(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", withSaleID(t, fixture(t, "sale_egp.json"), "11111111-1111-4111-8111-111111111111"))
	ctx := context.Background()
	secret := "password=secret postgres://user:pass@db sync_events(id=1) payload={\"x\":1} Traceback (most recent call last)"
	// Plant a sensitive processing error directly.
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash) VALUES ('88888888-8888-4888-8888-888888888888', $1, 'sale.finalized.v1', now(), now(), '{}', '\x00') ON CONFLICT DO NOTHING`, env.devID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status, attempt_count, last_error_code, last_error_message) VALUES ('88888888-8888-4888-8888-888888888888', 'sale_projection.v1', 'blocked', 1, 'SALE_ID_CONFLICT', $1) ON CONFLICT (event_id, processor) DO UPDATE SET status='blocked', last_error_code='SALE_ID_CONFLICT', last_error_message=$1`, secret); err != nil {
		t.Fatal(err)
	}
	health, err := env.dash.SyncHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(health)
	for _, leak := range []string{"password=secret", "user:pass@", "Traceback", "payload="} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("diagnostic leak %q in %s", leak, raw)
		}
	}
	if health.LastErrorCode != "SALE_ID_CONFLICT" {
		t.Fatalf("code preserved: %+v", health)
	}
	if health.LastErrorLabelAR == "" || health.LastErrorLabelEN == "" {
		t.Fatalf("mapped labels required: %+v", health)
	}
}

func TestM07QueueCountOnce(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", withSaleID(t, fixture(t, "sale_egp.json"), "11111111-1111-4111-8111-111111111111"))
	ctx := context.Background()
	// Force: one pending, one retry.
	if _, err := env.pool.Exec(ctx, `UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL WHERE processor='sale_projection.v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash) VALUES ('99999999-9999-4999-8999-999999999999', $1, 'sale.finalized.v1', now(), now(), '{}', '\x00') ON CONFLICT DO NOTHING`, env.devID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status, attempt_count, next_attempt_at) VALUES ('99999999-9999-4999-8999-999999999999', 'sale_projection.v1', 'retry', 1, now()) ON CONFLICT (event_id, processor) DO UPDATE SET status='retry', next_attempt_at=now()`); err != nil {
		t.Fatal(err)
	}
	health, err := env.dash.SyncHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.QueueCount != health.PendingCount+health.RetryCount {
		t.Fatalf("queue must equal pending+retry: %+v", health)
	}
	if health.QueueCount != 2 {
		t.Fatalf("want queue 2, got %+v", health)
	}
	// Fixture from spec: pending=0/retry=1 -> queue=1 tested via counts math.
}

func TestL06ActivityDeterministic(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	ts := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"} {
		if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash) VALUES ($1, $2, 'sale.finalized.v1', $3, $3, '{}', '\x00') ON CONFLICT DO NOTHING`, id, env.devID, ts); err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for i := 0; i < 5; i++ {
		items, err := env.dash.RecentActivity(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(items)
		if first == "" {
			first = string(raw)
		} else if string(raw) != first {
			t.Fatalf("activity order unstable:\n%s\n%s", first, raw)
		}
	}
	_ = first
}

func TestR05ActivityTopNPrefix(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	ts := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	ids := []string{
		"00000000-0000-4000-8000-000000000000",
		"11111111-1111-4111-8111-111111111111",
		"e1111111-1111-4111-8111-111111111111",
		"ffffffff-ffff-4fff-bfff-ffffffffffff",
	}
	for _, id := range ids {
		if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash) VALUES ($1, $2, 'sale.finalized.v1', $3, $3, '{}', '\x00') ON CONFLICT DO NOTHING`, id, env.devID, ts); err != nil {
			t.Fatal(err)
		}
	}
	at := func(limit int) []dashboard.ActivityItem {
		t.Helper()
		items, err := env.dash.RecentActivity(ctx, limit)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	one, two, four := at(1), at(2), at(4)
	if len(one) != 1 || len(two) != 2 || len(four) != 4 {
		t.Fatalf("lengths: %d %d %d", len(one), len(two), len(four))
	}
	for i := range one {
		if one[i].EventID != four[i].EventID {
			t.Fatalf("limit=1 not a prefix of limit=4: %v vs %v", one, four)
		}
	}
	for i := range two {
		if two[i].EventID != four[i].EventID {
			t.Fatalf("limit=2 not a prefix of limit=4: %v vs %v", two, four)
		}
	}
	// Deterministic by (ts, kind, event_id): accepted rows sort by event_id.
	for i := 1; i < len(four); i++ {
		if four[i-1].EventID >= four[i].EventID {
			t.Fatalf("not ordered by event_id: %v", four)
		}
	}
}

func TestH04DailyMissingFxFails(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", withSaleID(t, fixture(t, "sale_egp.json"), "11111111-1111-4111-8111-111111111111"))
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash) VALUES ('77777777-7777-4777-8777-777777777777', $1, 'sale.finalized.v1', '2026-09-20T10:00:00Z', now(), '{}', '\x00') ON CONFLICT DO NOTHING`, env.devID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en, currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at) VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01', '77777777-7777-4777-8777-777777777777', $1, 'MLR-X', 'STORE', '2026-09-20T10:00:00Z', '2026-09-20T10:00:00Z', 'a','b','c','d','e','f','g', 'USD', 1000, 0, 0, 1000, now()) ON CONFLICT DO NOTHING`, env.devID); err != nil {
		t.Fatal(err)
	}
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	if _, err := env.dash.Daily(ctx, req, "all"); err == nil {
		t.Fatal("daily All with missing FX must fail loudly")
	}
	// Native USD mode does not convert: must still work.
	usd, err := env.dash.Daily(ctx, req, "USD")
	if err != nil {
		t.Fatalf("native USD must work: %v", err)
	}
	if len(usd.Days) == 0 {
		t.Fatal("native USD days missing")
	}
}

func TestM01BranchNativeInvariant(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", withSale(t, fixture(t, "sale_egp.json"), "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", "2026-09-20T10:05:00Z"))
	projectDashSale(t, env, "66666666-6666-4666-8666-666666666666", "2026-09-20T11:00:00Z", withSale(t, fixture(t, "sale_egp.json"), "22222222-2222-4222-8222-222222222222", "2026-09-20T10:00:00Z", "2026-09-20T10:05:00Z"))
	ctx := context.Background()
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	sum, err := env.rep.Summary(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	branches, err := env.dash.Branches(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// Native EGP header totals must match across summary and branches.
	var want int64
	for _, b := range sum.CurrencyTotals {
		if b.Currency == "EGP" {
			want = b.SalesTotalMinor
		}
	}
	var got int64
	for _, r := range branches {
		if r.Currency != "EGP" {
			continue
		}
		var v int64
		if _, err := fmt.Sscanf(r.SalesTotalMinor, "%d", &v); err != nil {
			t.Fatal(err)
		}
		got += v
	}
	if got != want {
		t.Fatalf("branch native sum %d != summary %d", got, want)
	}
}

func TestH04MissingFxAllNormalizedEndpoints(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "55555555-5555-4555-8555-555555555555", "2026-09-20T10:00:00Z", withSale(t, fixture(t, "sale_egp.json"), "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", "2026-09-20T10:05:00Z"))
	ctx := context.Background()
	// One USD sale without FX (seeded; ingestion would never produce it).
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash) VALUES ('77777777-7777-4777-8777-777777777777', $1, 'sale.finalized.v1', '2026-09-20T10:00:00Z', now(), '{}', '\x00') ON CONFLICT DO NOTHING`, env.devID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en, currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at) VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01', '77777777-7777-4777-8777-777777777777', $1, 'MLR-X', 'STORE', '2026-09-20T10:00:00Z', '2026-09-20T10:00:00Z', 'a','b','c','d','e','f','g', 'USD', 1000, 0, 0, 1000, now()) ON CONFLICT DO NOTHING`, env.devID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO sale_lines_projection (sale_id, sale_item_id, position, sku, product_name, quantity, unit_price_minor, unit_currency, line_total_minor, line_currency) VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01', 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', 0, 'SKU-X', 'X', 1, 1000, 'USD', 1000, 'USD') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO sale_line_classifications_projection (sale_id, sale_item_id, classification_kind, classification_id, name_ar, name_en, position) VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01', 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', 'root', '00000000-0000-0000-0000-000000000101', 'x', 'X', 0) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	if _, err := env.dash.Overview(ctx, req); err == nil {
		t.Fatal("overview must fail on missing FX")
	}
	if _, err := env.dash.Daily(ctx, req, "all"); err == nil {
		t.Fatal("daily All must fail on missing FX")
	}
	if _, err := env.dash.ProductsNormalized(ctx, req); err == nil {
		t.Fatal("products All must fail on missing FX")
	}
	if _, err := env.dash.CategoriesNormalized(ctx, req, report.DimensionRootCategory); err == nil {
		t.Fatal("categories All must fail on missing FX")
	}
	// Native modes keep working (no conversion required).
	if _, err := env.dash.Daily(ctx, req, "USD"); err != nil {
		t.Fatalf("native USD daily must work: %v", err)
	}
}
