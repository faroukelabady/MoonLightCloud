package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

type stubAnalyticsRepo struct {
	Repository
	orders                              []OrderAnalyticsRowRaw
	counts                              []CatalogHealthCountRaw
	detail                              []CatalogHealthRowRaw
	lastStore, lastProvider, lastReason string
	lastLimit                           int
}

func (s *stubAnalyticsRepo) DashboardOrderAnalytics(_ context.Context, providerKey, storeID string, _, _ time.Time) ([]OrderAnalyticsRowRaw, error) {
	s.lastProvider, s.lastStore = providerKey, storeID
	return s.orders, nil
}
func (s *stubAnalyticsRepo) CatalogHealthSummaryRows(_ context.Context, storeID, providerKey string) ([]CatalogHealthCountRaw, error) {
	s.lastStore, s.lastProvider = storeID, providerKey
	return s.counts, nil
}
func (s *stubAnalyticsRepo) CatalogHealthDetailRows(_ context.Context, storeID, providerKey, reason string, limit int) ([]CatalogHealthRowRaw, error) {
	s.lastStore, s.lastProvider, s.lastReason, s.lastLimit = storeID, providerKey, reason, limit
	return s.detail, nil
}

func analyticsRequest() report.Request {
	req, err := report.NewService(stubAnalyticsRepo{}, clock.System{}, time.UTC).ParseRequest("today", "", "", "")
	if err != nil {
		panic(err)
	}
	return req
}

// §34/§39/§12/§124: online orders are operational metrics — never
// combined with Retail revenue, currencies never summed, active value
// covers exactly the documented status set, cancelled/deleted/refunded
// visible only in the breakdown.
func TestOrderAnalyticsStatusAndCurrencyRules(t *testing.T) {
	repo := &stubAnalyticsRepo{orders: []OrderAnalyticsRowRaw{
		{ProviderKey: "shopify-main", CanonicalStatus: "COMPLETED", Currency: "EGP", Orders: 2, ValueMinor: 100},
		{ProviderKey: "shopify-main", CanonicalStatus: "CANCELLED", Currency: "EGP", Orders: 1, ValueMinor: 500},
		{ProviderKey: "shopify-main", CanonicalStatus: "REFUNDED", Currency: "EGP", Orders: 1, ValueMinor: 700},
		{ProviderKey: "woo-main", CanonicalStatus: "PROCESSING", Currency: "EGP", Orders: 1, ValueMinor: 300},
		{ProviderKey: "woo-main", CanonicalStatus: "PENDING", Currency: "USD", Orders: 1, ValueMinor: 900},
	}}
	svc := NewService(report.NewService(repo, clock.System{}, time.UTC), repo, nil, clock.System{})
	out, err := svc.OrderAnalytics(context.Background(), analyticsRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	// EGP bucket: total 1600 across 5+1... active only COMPLETED+PROCESSING.
	var egp, usd *OrderCurrencyTotal
	for i := range out.CurrencyTotals {
		switch out.CurrencyTotals[i].Currency {
		case "EGP":
			egp = &out.CurrencyTotals[i]
		case "USD":
			usd = &out.CurrencyTotals[i]
		}
	}
	if egp == nil || usd == nil {
		t.Fatalf("currency buckets missing: %+v", out.CurrencyTotals)
	}
	// EGP: 5 orders/1600 total value (incl. cancelled+refunded);
	// active = COMPLETED(2)+PROCESSING(1) = 3 orders / 400.
	if egp.Orders != 5 || egp.ValueMinor != "1600" {
		t.Fatalf("EGP bucket: %+v", egp)
	}
	if egp.ActiveOrders != 3 || egp.ActiveValueMinor != "400" {
		t.Fatalf("EGP active semantics wrong: %+v", egp)
	}
	// USD: active (PENDING is active) 1/900.
	if usd.ActiveOrders != 1 || usd.ActiveValueMinor != "900" {
		t.Fatalf("USD active semantics wrong: %+v", usd)
	}
	// Money is exact string minor units — never a JSON float.
	if egp.ValueMinor != "1600" || usd.ValueMinor != "900" {
		t.Fatalf("money not exact strings: %v %v", egp.ValueMinor, usd.ValueMinor)
	}
	// Full status breakdown always visible (cancelled/refunded included).
	statuses := map[string]int64{}
	for _, s := range out.StatusCounts {
		statuses[s.CanonicalStatus] = s.Orders
	}
	if statuses["CANCELLED"] != 1 || statuses["REFUNDED"] != 1 || statuses["COMPLETED"] != 2 {
		t.Fatalf("status breakdown: %v", statuses)
	}
	// Providers never merge into one flag.
	providers := map[string]bool{}
	for _, p := range out.ProviderTotals {
		providers[p.ProviderKey] = true
	}
	if !providers["shopify-main"] || !providers["woo-main"] || len(providers) != 2 {
		t.Fatalf("provider totals: %v", out.ProviderTotals)
	}
}

// §127: provider filter narrows (validated + passed through), never
// falls back to all; invalid provider key is an explicit validation
// error.
func TestOrderAnalyticsProviderFilter(t *testing.T) {
	repo := &stubAnalyticsRepo{}
	svc := NewService(report.NewService(repo, clock.System{}, time.UTC), repo, nil, clock.System{})
	if _, err := svc.OrderAnalytics(context.Background(), analyticsRequest(), "shopify-main"); err != nil {
		t.Fatal(err)
	}
	if repo.lastProvider != "shopify-main" {
		t.Fatalf("provider filter dropped: %q", repo.lastProvider)
	}
	if _, err := svc.OrderAnalytics(context.Background(), analyticsRequest(), "Bad Key!"); err == nil {
		t.Fatal("malformed provider accepted")
	}
}

// §62/§150: stable health reason codes; §28-style limit validation;
// bounded detail with truncation flag; read-only pass-through scope.
func TestCatalogHealthCodesAndBounds(t *testing.T) {
	sku, name := "PAP-1", "Papyrus"
	repo := &stubAnalyticsRepo{
		counts: []CatalogHealthCountRaw{{ReasonCode: "CATALOG_MISSING_SKU", Products: 3}},
		detail: []CatalogHealthRowRaw{
			{ReasonCode: "CATALOG_MISSING_SKU", ProviderKey: "", SKU: sku, Name: name},
			{ReasonCode: "CATALOG_MISSING_SKU", ProviderKey: "", SKU: sku, Name: name},
		},
	}
	svc := NewService(report.NewService(repo, clock.System{}, time.UTC), repo, nil, clock.System{})
	req := analyticsRequest()
	out, err := svc.CatalogHealth(context.Background(), req, "shopify-main", "CATALOG_MISSING_SKU", 1)
	if err != nil {
		t.Fatal(err)
	}
	if out.ProviderKey != "shopify-main" || repo.lastProvider != "shopify-main" {
		t.Fatal("provider scope not applied")
	}
	if repo.lastReason != "CATALOG_MISSING_SKU" || repo.lastLimit != 2 {
		t.Fatalf("reason/limit not applied: %q %d", repo.lastReason, repo.lastLimit)
	}
	// Bounded: limit=1 with 2 rows → truncated, exactly 1 row kept.
	if len(out.Detail) != 1 || !out.DetailTruncated {
		t.Fatalf("detail bound: %d %+v", len(out.Detail), out.Detail)
	}
	// Stable vocabulary: every advertised code is a stable constant.
	if len(out.ReasonCodes) != len(CatalogHealthReasonCodes) {
		t.Fatalf("reason codes: %v", out.ReasonCodes)
	}
	for _, code := range out.ReasonCodes {
		if code == "" {
			t.Fatal("empty reason code")
		}
	}
	// Counts always include every reason (zero-filled): no invented score.
	if len(out.Counts) != len(CatalogHealthReasonCodes) {
		t.Fatalf("counts: %v", out.Counts)
	}
	// Validation: bad reason / bad limit explicit.
	if _, err := svc.CatalogHealth(context.Background(), req, "", "NOPE", 0); err == nil {
		t.Fatal("bad reason accepted")
	}
	if _, err := svc.CatalogHealth(context.Background(), req, "", "", 101); err == nil {
		t.Fatal("bad limit accepted")
	}
	if _, err := svc.CatalogHealth(context.Background(), req, "Bad Key!", "", 0); err == nil {
		t.Fatal("bad provider accepted")
	}
}
