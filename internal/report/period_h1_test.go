package report

import (
	"testing"
	"time"
)

func mustCairo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// cairoNoonInstant builds an unambiguous instant: noon local on a date.
func cairoNoonInstant(t *testing.T, date string, loc *time.Location) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", date+" 12:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// civilDatesIn enumerates wall dates covered by [startUTC, endUTC).
func civilDatesIn(t *testing.T, p Period, loc *time.Location) []string {
	t.Helper()
	var out []string
	for ts := p.StartUTC; ts.Before(p.EndUTC); ts = ts.Add(15 * time.Minute) {
		d := ts.In(loc).Format("2006-01-02")
		if len(out) == 0 || out[len(out)-1] != d {
			out = append(out, d)
		}
	}
	return out
}

func TestYesterdayImmediatelyAfterCairoSpringTransition(t *testing.T) {
	loc := mustCairo(t)
	// 2026-04-25 00:30 Africa/Cairo: yesterday must be exactly 2026-04-24.
	now, err := time.ParseInLocation("2006-01-02 15:04", "2026-04-25 00:30", loc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePeriod(PeriodYesterday, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	dates := civilDatesIn(t, p, loc)
	if len(dates) != 1 || dates[0] != "2026-04-24" {
		t.Fatalf("yesterday after spring transition: %v", dates)
	}
}

func TestLast10ImmediatelyAfterCairoSpringTransition(t *testing.T) {
	loc := mustCairo(t)
	now, err := time.ParseInLocation("2006-01-02 15:04", "2026-04-25 00:30", loc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePeriod(PeriodLast10CompletedDays, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	dates := civilDatesIn(t, p, loc)
	if len(dates) != 10 {
		t.Fatalf("want exactly 10 civil dates, got %v", dates)
	}
	if dates[0] != "2026-04-15" || dates[9] != "2026-04-24" {
		t.Fatalf("last-10 window wrong: %v", dates)
	}
}

func TestLast10StartAfterSpringTransition(t *testing.T) {
	loc := mustCairo(t)
	// 2026-05-04 00:30 Africa/Cairo: last-10 must start 2026-04-24.
	now, err := time.ParseInLocation("2006-01-02 15:04", "2026-05-04 00:30", loc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePeriod(PeriodLast10CompletedDays, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.StartLocal.In(loc).Format("2006-01-02"); got != "2026-04-24" {
		t.Fatalf("start civil date: %s", got)
	}
	if n := len(civilDatesIn(t, p, loc)); n != 10 {
		t.Fatalf("want 10 dates, got %d", n)
	}
}

func TestYesterdayImmediatelyAfterCairoFallTransition(t *testing.T) {
	loc := mustCairo(t)
	// Day after the fall-back date: yesterday is exactly the transition date.
	now, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-31 00:30", loc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePeriod(PeriodYesterday, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	dates := civilDatesIn(t, p, loc)
	if len(dates) != 1 || dates[0] != "2026-10-30" {
		t.Fatalf("yesterday after fall transition: %v", dates)
	}
}

func TestLast10ImmediatelyAfterCairoFallTransition(t *testing.T) {
	loc := mustCairo(t)
	now, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-31 00:30", loc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePeriod(PeriodLast10CompletedDays, "", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	dates := civilDatesIn(t, p, loc)
	if len(dates) != 10 {
		t.Fatalf("want exactly 10 civil dates, got %v", dates)
	}
	if dates[0] != "2026-10-21" || dates[9] != "2026-10-30" {
		t.Fatalf("last-10 window wrong: %v", dates)
	}
}

func TestRelativeCustomEquivalenceAroundTransitions(t *testing.T) {
	loc := mustCairo(t)
	for _, nowStr := range []string{"2026-04-25 00:30", "2026-05-04 00:30", "2026-10-31 00:30", "2026-09-22 10:00"} {
		now, err := time.ParseInLocation("2006-01-02 15:04", nowStr, loc)
		if err != nil {
			t.Fatal(err)
		}
		y, err := ResolvePeriod(PeriodYesterday, "", "", now, loc)
		if err != nil {
			t.Fatal(err)
		}
		yDate := y.StartLocal.In(loc).Format("2006-01-02")
		custom, err := ResolvePeriod(PeriodCustom, yDate, yDate, now, loc)
		if err != nil {
			t.Fatal(err)
		}
		if !y.StartUTC.Equal(custom.StartUTC) || !y.EndUTC.Equal(custom.EndUTC) {
			t.Fatalf("%s: yesterday %v..%v != custom %v..%v",
				nowStr, y.StartUTC, y.EndUTC, custom.StartUTC, custom.EndUTC)
		}
		l10, err := ResolvePeriod(PeriodLast10CompletedDays, "", "", now, loc)
		if err != nil {
			t.Fatal(err)
		}
		from := l10.StartLocal.In(loc).Format("2006-01-02")
		to := l10.EndLocalExclusive.In(loc).Add(-time.Minute).Format("2006-01-02")
		custom10, err := ResolvePeriod(PeriodCustom, from, to, now, loc)
		if err != nil {
			t.Fatal(err)
		}
		if !l10.StartUTC.Equal(custom10.StartUTC) || !l10.EndUTC.Equal(custom10.EndUTC) {
			t.Fatalf("%s: last10 %v..%v != custom %v..%v",
				nowStr, l10.StartUTC, l10.EndUTC, custom10.StartUTC, custom10.EndUTC)
		}
	}
}
