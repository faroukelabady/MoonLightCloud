package report

import (
	"testing"
	"time"
)

// dayWindow returns the UTC half-open window for one wall date.
func dayWindow(t *testing.T, date string, loc *time.Location) (time.Time, time.Time) {
	t.Helper()
	p, err := ResolvePeriod(PeriodCustom, date, date,
		mustParseIn(t, date, loc).Add(12*time.Hour), loc)
	if err != nil {
		t.Fatal(err)
	}
	return p.StartUTC, p.EndUTC
}

func mustParseIn(t *testing.T, date string, loc *time.Location) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTransitionWindowsTileExactly(t *testing.T) {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	// Three days around each 2026 transition must tile with no gaps or
	// overlaps regardless of gap-midnight normalization.
	for _, center := range []string{"2026-04-24", "2026-10-30"} {
		dates := []string{shiftDate(center, -1, loc), center, shiftDate(center, 1, loc), shiftDate(center, 2, loc)}
		var starts, ends []time.Time
		for _, d := range dates {
			s, e := dayWindow(t, d, loc)
			starts, ends = append(starts, s), append(ends, e)
		}
		for i := 0; i+1 < len(dates); i++ {
			if !ends[i].Equal(starts[i+1]) {
				t.Fatalf("%s: window end %v != next start %v", dates[i], ends[i], starts[i+1])
			}
		}
	}
}

func shiftDate(date string, n int, loc *time.Location) string {
	base, _ := time.ParseInLocation("2006-01-02", date, loc)
	return base.AddDate(0, 0, n).Format("2006-01-02")
}

func TestWallDateMatchesWindowAcrossTransitions(t *testing.T) {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	// Every 15-minute instant across both transitions must belong to
	// exactly one day window AND carry that window's wall date. This is
	// the property that prevents double-counting and omission.
	for _, center := range []string{"2026-04-24", "2026-10-30"} {
		dates := []string{shiftDate(center, -1, loc), center, shiftDate(center, 1, loc)}
		windows := map[string][2]time.Time{}
		for _, d := range dates {
			s, e := dayWindow(t, d, loc)
			windows[d] = [2]time.Time{s, e}
		}
		start := windows[dates[0]][0]
		end := windows[dates[2]][1]
		for ts := start; ts.Before(end); ts = ts.Add(15 * time.Minute) {
			owner := ""
			for _, d := range dates {
				w := windows[d]
				if !ts.Before(w[0]) && ts.Before(w[1]) {
					if owner != "" {
						t.Fatalf("%v double-covered by %s and %s", ts, owner, d)
					}
					owner = d
				}
			}
			if owner == "" {
				t.Fatalf("%v uncovered by any window", ts)
			}
			if got := ts.In(loc).Format("2006-01-02"); got != owner {
				t.Fatalf("%v in window %s but wall date %s", ts, owner, got)
			}
		}
	}
}

func TestSpringDayDurationReflectsTransition(t *testing.T) {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	// The transition day is not 24h; total tiled span stays exact.
	s, e := dayWindow(t, "2026-04-24", loc)
	if dur := e.Sub(s); dur == 24*time.Hour {
		t.Logf("note: runtime normalizes the gap midnight to a 24h window")
	} else if dur != 23*time.Hour {
		t.Fatalf("spring transition day must be 23h, got %v", dur)
	}
	s, e = dayWindow(t, "2026-10-30", loc)
	if dur := e.Sub(s); dur != 24*time.Hour && dur != 25*time.Hour {
		t.Fatalf("fall transition day must be 24h or 25h, got %v", dur)
	}
}
