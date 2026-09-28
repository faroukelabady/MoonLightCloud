package businessreports

import (
	"fmt"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// LoadCairo loads the single Phase 7B scheduling zone from the IANA
// database. Offsets are never hard-coded: Cairo transitions stay
// correct by date.
func LoadCairo() (*time.Location, error) {
	loc, err := time.LoadLocation(TimezoneCairo)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "report schedule", err)
	}
	return loc, nil
}

// civilDate parses a validated YYYY-MM-DD into report civil-date
// arithmetic (pure dates, never instants).
func civilDate(value string) (report.CivilDate, error) {
	if err := ValidateCivilDate(value); err != nil {
		return report.CivilDate{}, err
	}
	parsed, _ := time.Parse("2006-01-02", value)
	return report.CivilDate{Year: parsed.Year(), Month: parsed.Month(), Day: parsed.Day()}, nil
}

// SlotInstant resolves configured local HH:MM on a civil date to one
// instant. Slot identity is the date itself, so wall-clock ambiguity
// around transitions can never duplicate a slot.
func SlotInstant(slotDate, localTime string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, apperr.New(apperr.Internal, "report schedule: timezone not configured")
	}
	date, err := civilDate(slotDate)
	if err != nil {
		return time.Time{}, err
	}
	if err := ValidateLocalTime(localTime); err != nil {
		return time.Time{}, err
	}
	var hour, minute int
	if _, err := fmt.Sscanf(localTime, "%02d:%02d", &hour, &minute); err != nil {
		return time.Time{}, apperr.New(apperr.InvalidInput, "invalid local time: want HH:MM")
	}
	return time.Date(date.Year, date.Month, date.Day, hour, minute, 0, 0, loc), nil
}

// DailyPeriod returns the custom-period civil bounds for a DAILY slot
// on date D: the previous complete local day [D-1, D-1].
func DailyPeriod(slotDate string) (from, to string, err error) {
	date, err := civilDate(slotDate)
	if err != nil {
		return "", "", err
	}
	previous := date.AddDays(-1).String()
	return previous, previous, nil
}

// TenDayPeriod returns the custom-period civil bounds for a TEN_DAY
// slot on date D: ten complete local days [D-10, D-1]. Never 240
// elapsed hours.
func TenDayPeriod(slotDate string) (from, to string, err error) {
	date, err := civilDate(slotDate)
	if err != nil {
		return "", "", err
	}
	return date.AddDays(-10).String(), date.AddDays(-1).String(), nil
}

// PeriodBounds resolves one slot's half-open UTC query window from its
// civil bounds. Boundaries are local midnights via the frozen report
// helpers, so DST transitions stay exact.
func PeriodBounds(from, to string, loc *time.Location) (startUTC, endUTC time.Time, err error) {
	startDate, err := civilDate(from)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	endDate, err := civilDate(to)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := startDate.StartInstant(loc)
	end := endDate.AddDays(1).StartInstant(loc)
	return start.UTC(), end.UTC(), nil
}

// AdvanceSlotDate returns the next slot date after the given one:
// +1 calendar day for DAILY, +10 for TEN_DAY from its anchor chain.
func AdvanceSlotDate(kind ReportKind, anchor, slotDate string) (string, error) {
	date, err := civilDate(slotDate)
	if err != nil {
		return "", err
	}
	switch kind {
	case ReportDaily:
		return date.AddDays(1).String(), nil
	case ReportTenDay:
		if err := ValidateCivilDate(anchor); err != nil {
			return "", err
		}
		return date.AddDays(10).String(), nil
	default:
		return "", apperr.New(apperr.InvalidInput, "invalid report kind: want DAILY|TEN_DAY")
	}
}

// NextDailySlot returns the first slot date whose instant is strictly
// after the reference time.
func NextDailySlot(localTime string, after time.Time, loc *time.Location) (string, time.Time, error) {
	if loc == nil {
		return "", time.Time{}, apperr.New(apperr.Internal, "report schedule: timezone not configured")
	}
	if err := ValidateLocalTime(localTime); err != nil {
		return "", time.Time{}, err
	}
	local := after.In(loc)
	candidate := report.CivilDate{Year: local.Year(), Month: local.Month(), Day: local.Day()}
	for i := 0; i < 3; i++ {
		at, err := SlotInstant(candidate.String(), localTime, loc)
		if err != nil {
			return "", time.Time{}, err
		}
		if at.After(after) {
			return candidate.String(), at, nil
		}
		candidate = candidate.AddDays(1)
	}
	return "", time.Time{}, apperr.New(apperr.Internal, "report schedule: no future daily slot")
}

// NextTenDaySlot returns the first anchor-aligned slot date with an
// instant strictly after the reference time.
func NextTenDaySlot(anchor, localTime string, after time.Time, loc *time.Location) (string, time.Time, error) {
	if loc == nil {
		return "", time.Time{}, apperr.New(apperr.Internal, "report schedule: timezone not configured")
	}
	anchorDate, err := civilDate(anchor)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := ValidateLocalTime(localTime); err != nil {
		return "", time.Time{}, err
	}
	candidate := anchorDate
	for i := 0; i < 400; i++ {
		at, err := SlotInstant(candidate.String(), localTime, loc)
		if err != nil {
			return "", time.Time{}, err
		}
		if at.After(after) {
			return candidate.String(), at, nil
		}
		candidate = candidate.AddDays(10)
	}
	return "", time.Time{}, apperr.New(apperr.Internal, "report schedule: no future ten-day slot")
}

// SlotPlan is the computed materialization for one due schedule row:
// slot identity, custom-period civil bounds, and the half-open UTC
// window persisted on the run.
type SlotPlan struct {
	SlotDate    string
	PeriodFrom  string
	PeriodTo    string
	PeriodStart time.Time
	PeriodEnd   time.Time
}

// PlanSlot computes the exact slot for a schedule's stored
// next_run_local_date. Pure and deterministic: same row, same plan.
func PlanSlot(kind ReportKind, anchor, nextRunLocalDate string, loc *time.Location) (SlotPlan, error) {
	var from, to string
	var err error
	switch kind {
	case ReportDaily:
		from, to, err = DailyPeriod(nextRunLocalDate)
	case ReportTenDay:
		from, to, err = TenDayPeriod(nextRunLocalDate)
	default:
		return SlotPlan{}, apperr.New(apperr.InvalidInput, "invalid report kind: want DAILY|TEN_DAY")
	}
	if err != nil {
		return SlotPlan{}, err
	}
	start, end, err := PeriodBounds(from, to, loc)
	if err != nil {
		return SlotPlan{}, err
	}
	return SlotPlan{
		SlotDate: nextRunLocalDate, PeriodFrom: from, PeriodTo: to,
		PeriodStart: start, PeriodEnd: end,
	}, nil
}

// ManualPeriod resolves a run-now period from the current Cairo date:
// previous complete day (DAILY) or previous ten complete days
// (TEN_DAY). The current partial day is never included.
func ManualPeriod(kind ReportKind, now time.Time, loc *time.Location) (from, to string, err error) {
	if loc == nil {
		return "", "", apperr.New(apperr.Internal, "report schedule: timezone not configured")
	}
	local := now.In(loc)
	today := report.CivilDate{Year: local.Year(), Month: local.Month(), Day: local.Day()}.String()
	switch kind {
	case ReportDaily:
		return DailyPeriod(today)
	case ReportTenDay:
		return TenDayPeriod(today)
	default:
		return "", "", apperr.New(apperr.InvalidInput, "invalid report kind: want DAILY|TEN_DAY")
	}
}
