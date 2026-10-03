package report

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

// Phase 12 ranking tests. The frozen financial query remains the single
// ranking authority: rows are ranked deterministically and then truncated.
// Embedded nil interface: only the overridden methods are exercised.

type stubRankRepo struct {
	Repository
	products []ProductRow
	refunds  []RefundProductRow
}

func (s stubRankRepo) SalesByProduct(_ context.Context, _, _ time.Time, _ string) ([]ProductRow, error) {
	return s.products, nil
}
func (s stubRankRepo) RefundsByProduct(_ context.Context, _, _ time.Time, _ string) ([]RefundProductRow, error) {
	return s.refunds, nil
}
func (s stubRankRepo) SalesProjectionFreshness(_ context.Context) (FreshnessRow, error) {
	return FreshnessRow{}, nil
}
func (s stubRankRepo) ReturnProjectionFreshness(_ context.Context) (ReturnFreshnessRow, error) {
	return ReturnFreshnessRow{}, nil
}

func str(s string) *string { return &s }

// §28: explicit validation only — never silent clamping.
func TestParseLimitValidation(t *testing.T) {
	if v, err := ParseLimit(""); err != nil || v != 0 {
		t.Fatalf("empty limit: %v %d", err, v)
	}
	for raw, want := range map[string]int{"1": 1, "10": 10, "100": 100} {
		v, err := ParseLimit(raw)
		if err != nil || v != want {
			t.Fatalf("limit %q: %v %d", raw, err, v)
		}
	}
	for _, raw := range []string{"abc", "0", "-1", "101", "9999999999999999999", "1.5", " "} {
		if _, err := ParseLimit(raw); err == nil {
			t.Fatalf("limit %q accepted", raw)
		}
	}
}

// §144: deterministic ranking fixture — gross tie, different refunds,
// different units, negative net. Ranking metric = net line sales in a
// currency scope (never clamped: negative nets rank last but appear),
// units-desc otherwise, complete identity tie-break.
func TestTopRankingDeterministicOrder(t *testing.T) {
	// P1: gross 100, refund 0, net 100, units 2
	// P2: gross 100, refund 20, net 80, units 9 (gross tie with P1, loses on net)
	// P3: gross 50, refund 200, net -150, units 5 (negative net preserved)
	// P4: net 100, units 2 — ties P1 on net AND units; identity tie-break (id ASC)
	p1, p2, p3, p4 := str("P1"), str("P2"), str("P3"), str("P4")
	rows := []ProductRow{
		{ProductID: p1, SKU: "SKU-1", ProductName: "One", Units: 2, Currency: "EGP", LineSales: 10000},
		{ProductID: p2, SKU: "SKU-2", ProductName: "Two", Units: 9, Currency: "EGP", LineSales: 10000},
		{ProductID: p3, SKU: "SKU-3", ProductName: "Three", Units: 5, Currency: "EGP", LineSales: 5000},
		{ProductID: p4, SKU: "SKU-4", ProductName: "Four", Units: 2, Currency: "EGP", LineSales: 10000},
	}
	refunds := []RefundProductRow{
		{ProductID: p2, SKU: "SKU-2", ProductName: "Two", Units: 2, Currency: "EGP", Refund: 2000},
		{ProductID: p3, SKU: "SKU-3", ProductName: "Three", Units: 1, Currency: "EGP", Refund: 20000},
	}
	svc := NewService(stubRankRepo{products: rows, refunds: refunds}, clock.System{}, time.UTC)
	req := Request{Period: Period{Kind: "today", Timezone: "UTC", StartUTC: time.Unix(0, 0), EndUTC: time.Unix(100, 0)}, Currency: "EGP", Limit: 3, now: time.Unix(50, 0)}

	run := func() []string {
		out, err := svc.Breakdown(context.Background(), req, DimensionProduct)
		if err != nil {
			t.Fatal(err)
		}
		order := []string{}
		for _, r := range out.Rows {
			order = append(order, *r.ProductID)
		}
		return order
	}
	got := run()
	// net: P1=100, P4=100, P2=80, P3=-150 → P1 and P4 tie on net AND
	// units → product id ASC. Truncated to 3 (P3, the honest negative
	// net, is NOT shown beyond the limit but never clamped in data).
	want := []string{"P1", "P4", "P2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rank order = %v want %v", got, want)
		}
	}
	// Deterministic across reruns: equal rows never shuffle.
	for i := 0; i < 5; i++ {
		again := run()
		for j := range want {
			if again[j] != want[j] {
				t.Fatalf("nondeterministic ranking: %v vs %v", again, want)
			}
		}
	}

	// Unbounded (backward-compatible) keeps every row including the
	// negative-net row: canonical financial truth, never clamped.
	req.Limit = 0
	out, err := svc.Breakdown(context.Background(), req, DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 4 {
		t.Fatalf("rows = %d want 4", len(out.Rows))
	}
	last := out.Rows[len(out.Rows)-1]
	if *last.ProductID != "P3" {
		t.Fatalf("negative net row = %s want P3", *last.ProductID)
	}
	for _, bucket := range last.LineSales {
		if bucket.LineSalesMinor != 5000 || bucket.LineRefundMinor != 20000 {
			t.Fatalf("negative net not preserved: %+v", bucket)
		}
	}
}

// §12: currency buckets never merge — the all-currency scope keeps the
// frozen units-desc order and separate buckets.
func TestRankingCurrencySeparation(t *testing.T) {
	p1, p2 := str("P1"), str("P2")
	rows := []ProductRow{
		{ProductID: p1, SKU: "SKU-1", ProductName: "One", Units: 1, Currency: "EGP", LineSales: 90000},
		{ProductID: p1, SKU: "SKU-1", ProductName: "One", Units: 1, Currency: "USD", LineSales: 90000},
		{ProductID: p2, SKU: "SKU-2", ProductName: "Two", Units: 7, Currency: "EGP", LineSales: 100},
	}
	svc := NewService(stubRankRepo{products: rows}, clock.System{}, time.UTC)
	req := Request{Period: Period{Kind: "today", Timezone: "UTC", StartUTC: time.Unix(0, 0), EndUTC: time.Unix(100, 0)}, now: time.Unix(50, 0)}
	out, err := svc.Breakdown(context.Background(), req, DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 2 {
		t.Fatalf("rows = %d", len(out.Rows))
	}
	// All-currency: units-desc — P2 (7 units) first despite lower value.
	if *out.Rows[0].ProductID != "P2" {
		t.Fatalf("all-currency order = %s", *out.Rows[0].ProductID)
	}
	// Buckets stay separate per currency; no summed cross-currency total.
	for _, row := range out.Rows {
		if *row.ProductID == "P1" && len(row.LineSales) != 2 {
			t.Fatalf("currency buckets merged: %+v", row.LineSales)
		}
	}
}
