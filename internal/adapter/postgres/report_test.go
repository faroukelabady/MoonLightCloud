package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

var saleEventSeq = 100

func cairoLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func repService(env *saleEnv) report.Service {
	return report.NewService(NewDevices(env.pool, 5*time.Second), clock.System{}, cairoLocFor(env))
}

func cairoLocFor(env *saleEnv) *time.Location { return mustCairoLoc() }

var cachedCairo *time.Location

func mustCairoLoc() *time.Location {
	if cachedCairo == nil {
		loc, err := time.LoadLocation("Africa/Cairo")
		if err != nil {
			panic(err)
		}
		cachedCairo = loc
	}
	return cachedCairo
}

// projectSale ingests a sale payload (mutated from the EGP fixture) and
// projects it synchronously. Returns the event ID.
func projectSale(t *testing.T, env *saleEnv, saleID, occurred string, mutate func(map[string]any)) string {
	t.Helper()
	saleEventSeq++
	eventID := fmt.Sprintf("aaaaaaaa-aaaa-7aaa-8aaa-%012d", saleEventSeq)
	var m map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	m["occurred_at"] = occurred
	m["paid_at"] = occurred
	if mutate != nil {
		mutate(m)
	}
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":%q,"payload":%s}]}`,
		eventID, occurred, payload)
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadSaleEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load event: %v %v", ok, err)
	}
	res, err := store.ProjectSale(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if res.Outcome != sale.OutcomeProcessed && res.Outcome != sale.OutcomeAlready {
		t.Fatalf("projection outcome: %+v", res)
	}
	return eventID
}

func reportSummary(t *testing.T, env *saleEnv, kind, from, to, currency string) report.Summary {
	t.Helper()
	svc := repService(env)
	req, err := svc.ParseRequest(kind, from, to, currency)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	out, err := svc.Summary(context.Background(), req)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	return out
}

func TestReportSummaryExact(t *testing.T) {
	env := openSaleEnv(t)
	projectSale(t, env, "aaaaaaaa-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", nil)
	// USD sale via fixture file (its own sale_id 11111111-...).
	saleEventSeq++
	usdEvent := fmt.Sprintf("aaaaaaaa-aaaa-7aaa-8aaa-%012d", saleEventSeq)
	usdBody := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		usdEvent, fixture(t, "sale_usd.json"))
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(usdBody)); err != nil {
		t.Fatalf("ingest usd: %v", err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, _, _ := store.LoadSaleEvent(context.Background(), usdEvent)
	if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err != nil {
		t.Fatalf("project usd: %v", err)
	}

	sum := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	if sum.TransactionCount != 2 || sum.UnitsSold != 3 {
		t.Fatalf("counts: %+v", sum)
	}
	if len(sum.CurrencyTotals) != 2 {
		t.Fatalf("buckets: %+v", sum.CurrencyTotals)
	}
	// Alphabetical: EGP first.
	egp, usd := sum.CurrencyTotals[0], sum.CurrencyTotals[1]
	if egp.Currency != "EGP" || egp.SalesTotalMinor != 200000 || egp.SubtotalMinor != 200000 {
		t.Fatalf("egp bucket: %+v", egp)
	}
	if usd.Currency != "USD" || usd.SalesTotalMinor != 1250 || usd.DiscountMinor != 100 || usd.TaxMinor != 50 {
		t.Fatalf("usd bucket: %+v", usd)
	}
	if usd.LineCostMinor != 400 {
		t.Fatalf("usd line cost snapshot: %+v", usd)
	}
	if len(sum.PaymentTotals) != 3 {
		t.Fatalf("payments: %+v", sum.PaymentTotals)
	}
	if !sum.Freshness.CloudProjectionComplete || sum.Freshness.ProjectionBacklogCount != 0 {
		t.Fatalf("freshness: %+v", sum.Freshness)
	}
	if sum.Freshness.LatestSaleEventReceivedAt == nil || sum.Freshness.LatestProjectedSaleOccurredAt == nil {
		t.Fatalf("freshness timestamps: %+v", sum.Freshness)
	}
	if sum.Timezone != "Africa/Cairo" || sum.Period.Kind != "custom" {
		t.Fatalf("metadata: %+v", sum.Period)
	}
}

func TestReportMidnightBoundaries(t *testing.T) {
	env := openSaleEnv(t)
	// Cairo midnight 2026-09-22 = 2026-09-21T21:00:00Z.
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-21T20:59:00Z", nil)
	projectSale(t, env, "22222222-2222-4222-8222-222222222222", "2026-09-21T21:00:00Z", nil)
	projectSale(t, env, "33333333-3333-4333-8333-333333333333", "2026-09-21T21:01:00Z", nil)

	prev := reportSummary(t, env, "custom", "2026-09-21", "2026-09-21", "")
	next := reportSummary(t, env, "custom", "2026-09-22", "2026-09-22", "")
	if prev.TransactionCount != 1 || next.TransactionCount != 2 {
		t.Fatalf("half-open split: prev=%d next=%d", prev.TransactionCount, next.TransactionCount)
	}
}

func TestReportCairoVsUTCDate(t *testing.T) {
	env := openSaleEnv(t)
	// 2026-09-21T22:30:00Z is UTC Sep 21 but Cairo Sep 22 01:30.
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-21T22:30:00Z", nil)
	utcDay := reportSummary(t, env, "custom", "2026-09-21", "2026-09-21", "")
	if utcDay.TransactionCount != 0 {
		t.Fatalf("Cairo Sep 21 must exclude the sale, got %d", utcDay.TransactionCount)
	}
	cairoDay := reportSummary(t, env, "custom", "2026-09-22", "2026-09-22", "")
	if cairoDay.TransactionCount != 1 {
		t.Fatalf("Cairo Sep 22 must include the sale, got %d", cairoDay.TransactionCount)
	}
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-21", "2026-09-22", "")
	daily, err := svc.Daily(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily.Days) != 1 || daily.Days[0].Date != "2026-09-22" {
		t.Fatalf("daily grouping must use Cairo date: %+v", daily.Days)
	}
}

func TestReportNoData(t *testing.T) {
	env := openSaleEnv(t)
	sum := reportSummary(t, env, "custom", "2026-01-01", "2026-01-31", "")
	if sum.TransactionCount != 0 || sum.UnitsSold != 0 {
		t.Fatalf("zeros: %+v", sum)
	}
	if sum.CurrencyTotals == nil || sum.PaymentTotals == nil {
		t.Fatal("empty arrays, not null")
	}
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-01-01", "2026-01-31", "")
	daily, err := svc.Daily(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if daily.Days == nil {
		t.Fatal("days must be empty array, not null")
	}
	bd, err := svc.Breakdown(context.Background(), req, report.DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	if bd.Rows == nil {
		t.Fatal("rows must be empty array, not null")
	}
}

func TestReportCurrencyFilter(t *testing.T) {
	env := openSaleEnv(t)
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", nil)
	egp := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "EGP")
	if len(egp.CurrencyTotals) != 1 || egp.CurrencyTotals[0].Currency != "EGP" {
		t.Fatalf("filter: %+v", egp.CurrencyTotals)
	}
	svc := repService(env)
	if _, err := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "EUR"); err == nil {
		t.Fatal("unsupported currency must fail")
	}
}

func TestReportConflictLoserExcluded(t *testing.T) {
	env := openSaleEnv(t)
	payload := fixture(t, "sale_egp.json")
	mk := func(eventID string) {
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
			eventID, payload)
		if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	e1 := "22222222-2222-7222-8222-222222222222"
	e2 := "33333333-3333-7333-8333-333333333333"
	mk(e1)
	mk(e2)
	store := NewDevices(env.pool, 5*time.Second)
	for _, id := range []string{e1, e2} {
		rec, _, _ := store.LoadSaleEvent(context.Background(), id)
		if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err != nil {
			t.Fatalf("project %s: %v", id, err)
		}
	}
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
	sum, err := svc.Summary(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if sum.TransactionCount != 1 || sum.UnitsSold != 2 {
		t.Fatalf("loser must contribute nothing: %+v", sum)
	}
	if sum.Freshness.BlockedSaleEventCount != 1 {
		t.Fatalf("blocked count: %+v", sum.Freshness)
	}
}
