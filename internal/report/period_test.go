package report

import (
	"testing"
	"time"
)

func cairo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestToday(t *testing.T) {
	loc := cairo(t)
	// 2026-09-22 10:00 Cairo (UTC+3 in September) = 07:00Z.
	now := time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)
	p, err := ResolvePeriod(PeriodToday, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.StartUTC.Format(time.RFC3339); got != "2026-09-21T21:00:00Z" {
		t.Fatalf("today start UTC: %s", got)
	}
	if !p.EndUTC.Equal(now) {
		t.Fatalf("today end must be request now: %v", p.EndUTC)
	}
	if p.Timezone != "Africa/Cairo" {
		t.Fatalf("timezone: %s", p.Timezone)
	}
}

func TestYesterday(t *testing.T) {
	loc := cairo(t)
	now := time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)
	p, err := ResolvePeriod(PeriodYesterday, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.StartUTC.Format(time.RFC3339); got != "2026-09-20T21:00:00Z" {
		t.Fatalf("yesterday start: %s", got)
	}
	if got := p.EndUTC.Format(time.RFC3339); got != "2026-09-21T21:00:00Z" {
		t.Fatalf("yesterday end: %s", got)
	}
}

func TestLast10CompletedDays(t *testing.T) {
	loc := cairo(t)
	now := time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)
	p, err := ResolvePeriod(PeriodLast10CompletedDays, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	// [Sep 12 00:00 Cairo, Sep 22 00:00 Cairo): exactly 10 calendar dates.
	if got := p.StartLocal.Format("2006-01-02"); got != "2026-09-12" {
		t.Fatalf("start local: %s", got)
	}
	if got := p.EndLocalExclusive.Format("2006-01-02"); got != "2026-09-22" {
		t.Fatalf("end local: %s", got)
	}
	n := 0
	for d := p.StartLocal; d.Before(p.EndLocalExclusive); d = d.AddDate(0, 0, 1) {
		n++
	}
	if n != 10 {
		t.Fatalf("want 10 calendar dates, got %d", n)
	}
}

func TestCustomRanges(t *testing.T) {
	loc := cairo(t)
	now := time.Now().UTC()
	// One-day range: [Mar 5 00:00, Mar 6 00:00).
	p, err := ResolvePeriod(PeriodCustom, "2026-03-05", "2026-03-05", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.StartUTC.Format(time.RFC3339); got != "2026-03-04T22:00:00Z" {
		t.Fatalf("start: %s", got) // Cairo is UTC+2 in March
	}
	if got := p.EndUTC.Format(time.RFC3339); got != "2026-03-05T22:00:00Z" {
		t.Fatalf("end: %s", got)
	}
	// Month and year boundaries.
	p, err = ResolvePeriod(PeriodCustom, "2025-12-30", "2026-01-02", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for d := p.StartLocal; d.Before(p.EndLocalExclusive); d = d.AddDate(0, 0, 1) {
		n++
	}
	if n != 4 {
		t.Fatalf("want 4 dates across year boundary, got %d", n)
	}
	// Leap year: Feb 29 exists in 2024.
	if _, err := ResolvePeriod(PeriodCustom, "2024-02-29", "2024-02-29", now, loc); err != nil {
		t.Fatalf("leap day must be valid: %v", err)
	}
	// Nonexistent dates rejected (Go normalizes without error).
	for _, bad := range [][2]string{
		{"2026-02-30", "2026-02-30"},
		{"2026-13-01", "2026-13-01"},
		{"not-a-date", "2026-01-01"},
		{"2026-01-01", "2026-1-1"},
	} {
		if _, err := ResolvePeriod(PeriodCustom, bad[0], bad[1], now, loc); err == nil {
			t.Fatalf("invalid dates %v must fail", bad)
		}
	}
	if _, err := ResolvePeriod(PeriodCustom, "2026-02-02", "2026-02-01", now, loc); err == nil {
		t.Fatal("from > to must fail")
	}
	if _, err := ResolvePeriod(PeriodCustom, "2026-01-01", "", now, loc); err == nil {
		t.Fatal("missing boundary must fail")
	}
	if _, err := ResolvePeriod(PeriodCustom, "2025-01-01", "2026-01-02", now, loc); err == nil {
		t.Fatal("367-day range must exceed the 366-day maximum")
	}
	if _, err := ResolvePeriod(PeriodCustom, "2025-01-02", "2026-01-01", now, loc); err != nil {
		t.Fatalf("exactly 366 days must pass: %v", err)
	}
	if _, err := ResolvePeriod("weekly", "", "", now, loc); err == nil {
		t.Fatal("unsupported period must fail")
	}
}
