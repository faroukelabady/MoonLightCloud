package businessreports

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

func cairo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := LoadCairo()
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestSlotPeriods(t *testing.T) {
	from, to, err := DailyPeriod("2026-09-29")
	if err != nil || from != "2026-09-28" || to != "2026-09-28" {
		t.Fatalf("daily: %q %q %v", from, to, err)
	}
	from, to, err = TenDayPeriod("2026-10-11")
	if err != nil || from != "2026-10-01" || to != "2026-10-10" {
		t.Fatalf("ten-day: %q %q %v", from, to, err)
	}
}

func TestSlotInstantWallClock(t *testing.T) {
	loc := cairo(t)
	morning, err := SlotInstant("2026-09-29", "09:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	evening, err := SlotInstant("2026-09-29", "21:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	// Same slot date, different wall clocks: instants differ, and both
	// resolve the previous complete day as their period.
	for _, at := range []time.Time{morning, evening} {
		local := at.In(loc)
		if local.Format("2006-01-02") != "2026-09-29" || local.Hour() != at.In(loc).Hour() {
			t.Fatalf("wall clock: %v", at)
		}
	}
	from, to, err := DailyPeriod("2026-09-29")
	if err != nil || from != "2026-09-28" || to != "2026-09-28" {
		t.Fatalf("period independent of slot clock: %q %q", from, to)
	}
	// Start/end are exact local midnights in Cairo.
	start, end, err := PeriodBounds(from, to, loc)
	if err != nil {
		t.Fatal(err)
	}
	if start.In(loc).Format("2006-01-02 15:04") != "2026-09-28 00:00" {
		t.Fatalf("start: %v", start.In(loc))
	}
	if end.In(loc).Format("2006-01-02 15:04") != "2026-09-29 00:00" {
		t.Fatalf("end: %v", end.In(loc))
	}
	if end.Sub(start) != 24*time.Hour {
		t.Fatalf("normal day is 24h: %v", end.Sub(start))
	}
}

func TestDSTCalendarSemantics(t *testing.T) {
	loc := cairo(t)
	// Scheduled slot periods must equal the canonical report API
	// custom periods for identical civil bounds, across Cairo offset
	// transitions (Apr/Oct 2026): same instants, half-open, DST-exact.
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, slot := range []string{
		"2026-04-23", "2026-04-24", "2026-04-25",
		"2026-10-29", "2026-10-30", "2026-10-31",
		"2026-09-29",
	} {
		from, to, err := DailyPeriod(slot)
		if err != nil {
			t.Fatal(err)
		}
		mine_start, mine_end, err := PeriodBounds(from, to, loc)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := report.ResolvePeriod(report.PeriodCustom, from, to, now, loc)
		if err != nil {
			t.Fatal(err)
		}
		if !mine_start.Equal(canonical.StartUTC) || !mine_end.Equal(canonical.EndUTC) {
			t.Fatalf("slot %s: mine [%v,%v) canonical [%v,%v)",
				slot, mine_start, mine_end, canonical.StartUTC, canonical.EndUTC)
		}
		// Local identity is one calendar day regardless of elapsed hours.
		if canonical.StartLocal.Format("2006-01-02") != from ||
			canonical.EndLocalExclusive.Format("2006-01-02") != toDatePlusOne(from, t) {
			t.Fatalf("slot %s local identity", slot)
		}
		elapsed := mine_end.Sub(mine_start)
		if elapsed != 23*time.Hour && elapsed != 24*time.Hour && elapsed != 25*time.Hour {
			t.Fatalf("slot %s elapsed %v", slot, elapsed)
		}
	}
	// Ten days crossing the spring transition are exactly ten local dates.
	plan, err := PlanSlot(ReportTenDay, "2026-04-20", "2026-04-30", loc)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PeriodFrom != "2026-04-20" || plan.PeriodTo != "2026-04-29" {
		t.Fatalf("ten-day dst bounds: %+v", plan)
	}
	canonical, err := report.ResolvePeriod(report.PeriodCustom, plan.PeriodFrom, plan.PeriodTo, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PeriodStart.Equal(canonical.StartUTC) || !plan.PeriodEnd.Equal(canonical.EndUTC) {
		t.Fatalf("ten-day dst mismatch")
	}
}

func toDatePlusOne(from string, t *testing.T) string {
	t.Helper()
	date, err := civilDate(from)
	if err != nil {
		t.Fatal(err)
	}
	return date.AddDays(1).String()
}

func TestTenDayAnchorChain(t *testing.T) {
	loc := cairo(t)
	after := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	date, at, err := NextTenDaySlot("2026-10-01", "21:00", after, loc)
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-10-01" {
		t.Fatalf("anchor first: %s", date)
	}
	if at.In(loc).Format("15:04") != "21:00" {
		t.Fatalf("wall clock preserved: %v", at.In(loc))
	}
	next, err := AdvanceSlotDate(ReportTenDay, "2026-10-01", date)
	if err != nil || next != "2026-10-11" {
		t.Fatalf("anchor +10: %s %v", next, err)
	}
	next2, err := AdvanceSlotDate(ReportTenDay, "2026-10-01", next)
	if err != nil || next2 != "2026-10-21" {
		t.Fatalf("anchor +20: %s %v", next2, err)
	}
}

func TestManualPeriod(t *testing.T) {
	loc := cairo(t)
	now := time.Date(2026, 9, 28, 15, 30, 0, 0, time.UTC)
	from, to, err := ManualPeriod(ReportDaily, now, loc)
	if err != nil || from != "2026-09-27" || to != "2026-09-27" {
		t.Fatalf("manual daily: %q %q %v", from, to, err)
	}
	from, to, err = ManualPeriod(ReportTenDay, now, loc)
	if err != nil || from != "2026-09-18" || to != "2026-09-27" {
		t.Fatalf("manual ten-day: %q %q %v", from, to, err)
	}
}

// stubReportSource serves canned canonical summaries.
type stubReportSource struct {
	summary report.Summary
	err     error
}

func (s *stubReportSource) ParseRequest(kind, fromDate, toDate, currency string) (report.Request, error) {
	period, err := report.ResolvePeriod(kind, fromDate, toDate, time.Now().UTC(), cairoLoc())
	if err != nil {
		return report.Request{}, err
	}
	return report.Request{Period: period, Currency: currency}, nil
}

func cairoLoc() *time.Location {
	loc, err := time.LoadLocation(TimezoneCairo)
	if err != nil {
		panic(err)
	}
	return loc
}

func (s *stubReportSource) Summary(_ context.Context, _ report.Request) (report.Summary, error) {
	return s.summary, s.err
}

func canonicalSummary() report.Summary {
	at := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	return report.Summary{
		TransactionCount: 5, UnitsSold: 7,
		ReturnTransactionCount: 1, UnitsReturned: 2,
		CurrencyTotals: []report.CurrencyTotal{
			{Currency: "USD", SalesTotalMinor: 2500, RefundTotalMinor: 0, NetSalesMinor: 2500, LineCostMinor: 1000, NetCostMinor: 1000},
			{Currency: "EGP", SalesTotalMinor: 125000, RefundTotalMinor: 5000, NetSalesMinor: 120000, LineCostMinor: 80000, NetCostMinor: 79000},
		},
		Freshness: report.Freshness{LatestProjectedSaleOccurredAt: &at},
	}
}

func TestComposerDeterministic(t *testing.T) {
	ctx := context.Background()
	source := &stubReportSource{summary: canonicalSummary()}
	first, firstFP, err := ComposeReport(ctx, source, ReportDaily, "2026-09-28", "2026-09-28", LocaleAR)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		body, fp, err := ComposeReport(ctx, source, ReportDaily, "2026-09-28", "2026-09-28", LocaleAR)
		if err != nil || body != first || fp != firstFP {
			t.Fatalf("nondeterministic at %d", i)
		}
	}
	for _, want := range []string{
		"تقرير المبيعات اليومي", "الفترة: 2026-09-28", "المعاملات: 5",
		"EGP 1250.00", "EGP 50.00", "EGP 1200.00", "EGP 790.00",
		"USD 25.00", "بيانات السحابة حتى:",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("missing %q in:\n%s", want, first)
		}
	}
	if len(first) > 1024 {
		t.Fatalf("body bound: %d", len(first))
	}
	english, _, err := ComposeReport(ctx, source, ReportDaily, "2026-09-28", "2026-09-28", LocaleEN)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Daily Business Report", "Period: 2026-09-28", "Transactions: 5", "Gross sales:"} {
		if !strings.Contains(english, want) {
			t.Fatalf("missing %q in:\n%s", want, english)
		}
	}
	tenDay, _, err := ComposeReport(ctx, source, ReportTenDay, "2026-09-18", "2026-09-27", LocaleAR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tenDay, "10 أيام") || !strings.Contains(tenDay, "2026-09-18 - 2026-09-27") {
		t.Fatalf("ten-day body:\n%s", tenDay)
	}
}

func TestComposerExactMoney(t *testing.T) {
	if got := formatMinor(9007199254740993); got != "90071992547409.93" {
		t.Fatalf("exact: %s", got)
	}
	if got := formatMinor(-1050); got != "-10.50" {
		t.Fatalf("negative: %s", got)
	}
	if got := formatMinor(0); got != "0.00" {
		t.Fatalf("zero: %s", got)
	}
}

func TestComposerTooLarge(t *testing.T) {
	ctx := context.Background()
	buckets := make([]report.CurrencyTotal, 0, 40)
	for i := 0; i < 40; i++ {
		buckets = append(buckets, report.CurrencyTotal{Currency: "EGP", SalesTotalMinor: int64(i)})
	}
	source := &stubReportSource{summary: report.Summary{CurrencyTotals: buckets}}
	_, _, err := ComposeReport(ctx, source, ReportDaily, "2026-09-28", "2026-09-28", LocaleAR)
	if err == nil {
		t.Fatal("oversized body must block")
	}
}

func TestNextTenDayOldAnchors(t *testing.T) {
	loc := cairo(t)
	reference := time.Date(2026, 9, 28, 15, 30, 0, 0, time.UTC)
	for _, anchor := range []string{"2000-01-01", "1970-01-01", "2020-02-29"} {
		slot, at, err := NextTenDaySlot(anchor, "21:00", reference, loc)
		if err != nil {
			t.Fatalf("anchor %s: %v", anchor, err)
		}
		anchorDate, _ := civilDate(anchor)
		slotDate, _ := civilDate(slot)
		if civilDayDifference(anchorDate, slotDate)%10 != 0 {
			t.Fatalf("anchor %s slot %s misaligned", anchor, slot)
		}
		if !at.After(reference) {
			t.Fatalf("anchor %s slot not future: %v", anchor, at)
		}
		// Previous aligned slot is at or before the reference.
		previous := slotDate.AddDays(-10)
		previousAt, err := SlotInstant(previous.String(), "21:00", loc)
		if err != nil {
			t.Fatal(err)
		}
		if previousAt.After(reference) && compareCivilDate(anchorDate, slotDate) != 0 {
			t.Fatalf("anchor %s: previous %v after reference", anchor, previousAt)
		}
		t.Logf("anchor %s -> slot %s", anchor, slot)
	}
}

func TestNextTenDaySlotEdges(t *testing.T) {
	loc := cairo(t)
	// Reference before the anchor: anchor itself is next.
	reference := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	slot, _, err := NextTenDaySlot("2026-10-01", "21:00", reference, loc)
	if err != nil || slot != "2026-10-01" {
		t.Fatalf("future anchor: %s %v", slot, err)
	}
	// Same aligned date before wall clock: today.
	morning := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	slot, _, err = NextTenDaySlot("2026-10-01", "21:00", morning, loc)
	if err != nil || slot != "2026-10-01" {
		t.Fatalf("same date before time: %s %v", slot, err)
	}
	// Same aligned date after wall clock: +10.
	evening := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	slot, _, err = NextTenDaySlot("2026-10-01", "21:00", evening, loc)
	if err != nil || slot != "2026-10-11" {
		t.Fatalf("same date after time: %s %v", slot, err)
	}
	// DST crossing preserves civil alignment and wall clock.
	slot, at, err := NextTenDaySlot("2026-04-20", "21:00",
		time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), loc)
	if err != nil || slot != "2026-04-30" {
		t.Fatalf("dst alignment: %s %v", slot, err)
	}
	if at.In(loc).Format("15:04") != "21:00" {
		t.Fatalf("dst wall clock: %v", at.In(loc))
	}
}
