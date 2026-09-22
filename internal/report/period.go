// Package report is the reusable Cloud reporting domain for historical
// finalized Sales. Business calculations live here — never in HTTP
// handlers, dashboard code, or future notification jobs — so Phase 3B
// dashboards and Phase 7B scheduled WhatsApp reports share one authority.
//
// Source of truth is always the sale.finalized.v1 projection
// (sales_projection + children). Current product/category/user state is
// never consulted; transport timestamps never decide business days.
package report

import (
	"fmt"
	"regexp"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

// Supported period kinds.
const (
	PeriodToday               = "today"
	PeriodYesterday           = "yesterday"
	PeriodLast10CompletedDays = "last_10_completed_days"
	PeriodCustom              = "custom"
)

// MaxCustomRangeDays bounds custom ranges (documented, inclusive dates).
const MaxCustomRangeDays = 366

var dateFormat = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Period is a resolved business period: local calendar boundaries with the
// UTC half-open query window [StartUTC, EndUTC). Local days are calendar
// days via AddDate — never 24-hour arithmetic — so DST transitions stay
// correct.
type Period struct {
	Kind              string
	Timezone          string
	StartLocal        time.Time
	EndLocalExclusive time.Time
	StartUTC          time.Time
	EndUTC            time.Time
}

// CivilDate is a store-local calendar date without wall-clock tricks.
// Relative business periods move on civil dates first and resolve instants
// second: subtracting calendar days from a UTC instant and re-inferring the
// local date selects the wrong civil day around DST transitions (the UTC
// date and the Cairo date can differ by one).
type CivilDate struct {
	Year  int
	Month time.Month
	Day   int
}

// civilDateFromInstant extracts the store-local civil date of one instant.
func civilDateFromInstant(now time.Time, loc *time.Location) CivilDate {
	local := now.In(loc)
	return CivilDate{Year: local.Year(), Month: local.Month(), Day: local.Day()}
}

// AddDays moves by whole calendar days (pure date arithmetic, no instants).
func (d CivilDate) AddDays(n int) CivilDate {
	y, m, day := time.Date(d.Year, d.Month, d.Day+n, 0, 0, 0, 0, time.UTC).Date()
	return CivilDate{Year: y, Month: m, Day: day}
}

// StartInstant resolves the exact first instant belonging to this civil
// date (gap-safe midnight handling).
func (d CivilDate) StartInstant(loc *time.Location) time.Time {
	return startOfLocalDay(time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc), loc)
}

// String renders YYYY-MM-DD.
func (d CivilDate) String() string {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// parseCivilDate validates YYYY-MM-DD and rejects nonexistent dates
// (time.Date normalizes Feb 30, so the round-trip must match exactly).
func parseCivilDate(s string) (CivilDate, error) {
	if !dateFormat.MatchString(s) {
		return CivilDate{}, fmt.Errorf("must be YYYY-MM-DD")
	}
	var y, m, day int
	if _, err := fmt.Sscanf(s, "%04d-%02d-%02d", &y, &m, &day); err != nil {
		return CivilDate{}, fmt.Errorf("must be YYYY-MM-DD")
	}
	got := time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
	if got.Format("2006-01-02") != s {
		return CivilDate{}, fmt.Errorf("nonexistent calendar date")
	}
	return CivilDate{Year: y, Month: time.Month(m), Day: day}, nil
}

// ResolvePeriod validates the request and resolves calendar boundaries in
// loc using one captured now instant (clock-injected for determinism).
func ResolvePeriod(kind, fromDate, toDate string, now time.Time, loc *time.Location) (Period, error) {
	if loc == nil {
		return Period{}, apperr.New(apperr.Internal, "store timezone not configured")
	}
	today := civilDateFromInstant(now, loc)
	todayStart := today.StartInstant(loc)
	switch kind {
	case PeriodToday:
		return Period{
			Kind: kind, Timezone: loc.String(),
			StartLocal: todayStart, EndLocalExclusive: now.In(loc),
			StartUTC: todayStart.UTC(), EndUTC: now.UTC(),
		}, nil
	case PeriodYesterday:
		// Civil date − 1 day, then resolve: never AddDate on the instant.
		start := today.AddDays(-1).StartInstant(loc)
		return Period{
			Kind: kind, Timezone: loc.String(),
			StartLocal: start, EndLocalExclusive: todayStart,
			StartUTC: start.UTC(), EndUTC: todayStart.UTC(),
		}, nil
	case PeriodLast10CompletedDays:
		// Exactly 10 completed civil dates: [today−10d, today).
		start := today.AddDays(-10).StartInstant(loc)
		return Period{
			Kind: kind, Timezone: loc.String(),
			StartLocal: start, EndLocalExclusive: todayStart,
			StartUTC: start.UTC(), EndUTC: todayStart.UTC(),
		}, nil
	case PeriodCustom:
		return resolveCustom(fromDate, toDate, loc)
	default:
		return Period{}, apperr.New(apperr.InvalidInput,
			fmt.Sprintf("unsupported period %q: want today|yesterday|last_10_completed_days|custom", kind))
	}
}

func resolveCustom(fromDate, toDate string, loc *time.Location) (Period, error) {
	if fromDate == "" || toDate == "" {
		return Period{}, apperr.New(apperr.InvalidInput, "custom period requires from_date and to_date (YYYY-MM-DD)")
	}
	from, err := parseCivilDate(fromDate)
	if err != nil {
		return Period{}, apperr.New(apperr.InvalidInput, fmt.Sprintf("invalid from_date %q", fromDate))
	}
	to, err := parseCivilDate(toDate)
	if err != nil {
		return Period{}, apperr.New(apperr.InvalidInput, fmt.Sprintf("invalid to_date %q", toDate))
	}
	if toDate < fromDate {
		return Period{}, apperr.New(apperr.InvalidInput, "from_date must not be after to_date")
	}
	// Count civil dates by stepping (exact across DST).
	days := 0
	for d := from; ; d = d.AddDays(1) {
		days++
		if days > MaxCustomRangeDays {
			return Period{}, apperr.New(apperr.InvalidInput,
				fmt.Sprintf("custom range exceeds maximum of %d calendar days", MaxCustomRangeDays))
		}
		if d == to {
			break
		}
	}
	start := from.StartInstant(loc)
	end := to.AddDays(1).StartInstant(loc)
	return Period{
		Kind: PeriodCustom, Timezone: loc.String(),
		StartLocal: start, EndLocalExclusive: end,
		StartUTC: start.UTC(), EndUTC: end.UTC(),
	}, nil
}

// startOfLocalDay returns the exact first instant of a local calendar date:
// the smallest instant whose wall-clock date in loc is that date. Plain
// midnight construction is wrong around DST gaps (a nonexistent 00:00 may
// normalize an hour early, which would misassign late-evening sales to the
// next business day versus SQL wall-date grouping). The forward walk finds
// the true transition instant; unambiguous midnights cost one check.
func startOfLocalDay(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	guess := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if wallDate(guess) == wallDateOf(local) {
		return guess
	}
	// Nonexistent midnight: walk forward to the first instant carrying the
	// target date (bounded; real transitions shift by minutes, not days).
	want := wallDateOf(local)
	for i := 0; i < 24*60; i++ {
		guess = guess.Add(time.Minute)
		if wallDate(guess) == want {
			return guess
		}
	}
	return guess
}

func wallDate(t time.Time) string { return t.Format("2006-01-02") }

func wallDateOf(local time.Time) string { return local.Format("2006-01-02") }

// Now captures one request instant for all period calculations.
func Now(c clock.Clock) time.Time { return c.Now() }
