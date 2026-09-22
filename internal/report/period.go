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

// ResolvePeriod validates the request and resolves calendar boundaries in
// loc using one captured now instant (clock-injected for determinism).
func ResolvePeriod(kind, fromDate, toDate string, now time.Time, loc *time.Location) (Period, error) {
	if loc == nil {
		return Period{}, apperr.New(apperr.Internal, "store timezone not configured")
	}
	today := startOfLocalDay(now, loc)
	switch kind {
	case PeriodToday:
		return Period{
			Kind: kind, Timezone: loc.String(),
			StartLocal: today, EndLocalExclusive: now.In(loc),
			StartUTC: today.UTC(), EndUTC: now.UTC(),
		}, nil
	case PeriodYesterday:
		// Shift the input, then construct: AddDate on an already-corrected
		// boundary would reintroduce gap error.
		start := startOfLocalDay(now.AddDate(0, 0, -1), loc)
		return Period{
			Kind: kind, Timezone: loc.String(),
			StartLocal: start, EndLocalExclusive: today,
			StartUTC: start.UTC(), EndUTC: today.UTC(),
		}, nil
	case PeriodLast10CompletedDays:
		// Exactly 10 completed calendar dates: [today-10d, today).
		start := startOfLocalDay(now.AddDate(0, 0, -10), loc)
		return Period{
			Kind: kind, Timezone: loc.String(),
			StartLocal: start, EndLocalExclusive: today,
			StartUTC: start.UTC(), EndUTC: today.UTC(),
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
	if !dateFormat.MatchString(fromDate) || !dateFormat.MatchString(toDate) {
		return Period{}, apperr.New(apperr.InvalidInput, "custom dates must be YYYY-MM-DD")
	}
	from, err := time.ParseInLocation("2006-01-02", fromDate, loc)
	if err != nil {
		return Period{}, apperr.New(apperr.InvalidInput, fmt.Sprintf("invalid from_date %q", fromDate))
	}
	to, err := time.ParseInLocation("2006-01-02", toDate, loc)
	if err != nil {
		return Period{}, apperr.New(apperr.InvalidInput, fmt.Sprintf("invalid to_date %q", toDate))
	}
	// Reject nonexistent calendar dates (e.g. 2026-02-30 normalizes).
	if from.Format("2006-01-02") != fromDate {
		return Period{}, apperr.New(apperr.InvalidInput, fmt.Sprintf("invalid from_date %q", fromDate))
	}
	if to.Format("2006-01-02") != toDate {
		return Period{}, apperr.New(apperr.InvalidInput, fmt.Sprintf("invalid to_date %q", toDate))
	}
	if from.After(to) {
		return Period{}, apperr.New(apperr.InvalidInput, "from_date must not be after to_date")
	}
	// Count calendar days by wall-date stepping from the corrected start
	// (exact across DST; 24h math would miscount transition days).
	days := 0
	cur := startOfLocalDay(from, loc)
	for {
		days++
		if days > MaxCustomRangeDays {
			return Period{}, apperr.New(apperr.InvalidInput,
				fmt.Sprintf("custom range exceeds maximum of %d calendar days", MaxCustomRangeDays))
		}
		if cur.Format("2006-01-02") == toDate {
			break
		}
		y, m, dd := cur.Date()
		cur = startOfLocalDay(time.Date(y, m, dd+1, 0, 0, 0, 0, loc), loc)
	}
	end := endOfCustomRange(to, loc)
	return Period{
		Kind: PeriodCustom, Timezone: loc.String(),
		StartLocal: startOfLocalDay(from, loc), EndLocalExclusive: end,
		StartUTC: startOfLocalDay(from, loc).UTC(), EndUTC: end.UTC(),
	}, nil
}

// endOfCustomRange is the exact start of the day after toDate, constructed
// from date components (never by shifting an instant, which would preserve
// wall-clock time instead of landing on midnight).
func endOfCustomRange(to time.Time, loc *time.Location) time.Time {
	y, m, d := to.Date()
	return startOfLocalDay(time.Date(y, m, d+1, 0, 0, 0, 0, loc), loc)
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
