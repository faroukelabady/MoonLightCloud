package businessreports

import (
	"context"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// GetRecipient reveals an address only to the guarded configuration editor.
func (s *Service) GetRecipient(ctx context.Context, id string) (Recipient, error) {
	r, found, err := s.recipients.GetRecipient(ctx, id)
	if err != nil {
		return Recipient{}, err
	}
	if !found {
		return Recipient{}, apperr.New(apperr.NotFound, "recipient not found")
	}
	return r, nil
}

func (s *Service) UpdateRecipient(ctx context.Context, id, label, address, locale string) (Recipient, error) {
	label = strings.TrimSpace(label)
	if len(label) == 0 || len(label) > 64 {
		return Recipient{}, apperr.New(apperr.InvalidInput, "invalid recipient label: want 1..64 characters")
	}
	if err := notifications.ValidateRecipient(address); err != nil {
		return Recipient{}, apperr.New(apperr.InvalidInput, "INVALID_RECIPIENT")
	}
	if err := ValidateLocale(locale); err != nil {
		return Recipient{}, err
	}
	return s.recipients.UpdateRecipient(ctx, id, label, address, locale)
}

func (s *Service) DeleteRecipient(ctx context.Context, id string) error {
	return s.recipients.DeleteRecipient(ctx, id)
}

// Editing changes future slots/recipients only. The store locks the schedule
// and checks the revision, so concurrent edits never silently overwrite.
func (s *Service) UpdateSchedule(ctx context.Context, id, name, kind, localTime, anchor string, recipients []string, revision int64) (Schedule, error) {
	current, found, err := s.schedules.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if !found || current.Deleted {
		return Schedule{}, apperr.New(apperr.NotFound, "schedule not found")
	}
	if revision != current.Revision {
		return Schedule{}, apperr.New(apperr.Conflict, "SCHEDULE_CHANGED")
	}
	name = strings.TrimSpace(name)
	if len(name) == 0 || len(name) > 64 {
		return Schedule{}, apperr.New(apperr.InvalidInput, "invalid schedule name: want 1..64 characters")
	}
	rkind, err := ValidateReportKind(kind)
	if err != nil {
		return Schedule{}, err
	}
	if err := ValidateLocalTime(localTime); err != nil {
		return Schedule{}, err
	}
	if rkind == ReportTenDay {
		if err := ValidateCivilDate(anchor); err != nil {
			return Schedule{}, apperr.New(apperr.InvalidInput, "ten-day schedule requires anchor_local_date")
		}
	} else {
		anchor = ""
	}
	if len(recipients) == 0 {
		return Schedule{}, apperr.New(apperr.InvalidInput, CodeNoRecipients)
	}
	if current.Kind != rkind || current.LocalTime != localTime || current.AnchorLocalDate != anchor {
		current.NextRunLocalDate, current.NextRunAt, err = s.firstFutureSlot(rkind, anchor, localTime, s.clock.Now())
		if err != nil {
			return Schedule{}, err
		}
	}
	current.Name, current.Kind, current.LocalTime, current.AnchorLocalDate = name, rkind, localTime, anchor
	if err := s.schedules.UpdateScheduleWithRecipients(ctx, current, recipients, revision); err != nil {
		return Schedule{}, err
	}
	// Return the locked store's cursor, which a planner may have advanced
	// while the editor was open.
	updated, found, err := s.schedules.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if !found || updated.Deleted {
		return Schedule{}, apperr.New(apperr.NotFound, "schedule not found")
	}
	return updated, nil
}

func (s *Service) DeleteSchedule(ctx context.Context, id string) error {
	return s.schedules.DeleteSchedule(ctx, id)
}

type RunPage struct {
	Runs          []Run
	NextCursor    string
	RemainingRuns int64
}

// Keyset pagination stays stable when new reports arrive between pages.
func (s *Service) ListRunPage(ctx context.Context, beforeID string, limit int32) (RunPage, error) {
	if limit < 1 || limit > 50 {
		return RunPage{}, apperr.New(apperr.InvalidInput, "page size must be 1..50")
	}
	if beforeID != "" {
		if _, found, err := s.runs.GetRun(ctx, beforeID); err != nil {
			return RunPage{}, err
		} else if !found {
			return RunPage{}, apperr.New(apperr.InvalidInput, "invalid report cursor")
		}
	}
	runs, remaining, err := s.runs.ListRunPage(ctx, beforeID, limit+1)
	if err != nil {
		return RunPage{}, err
	}
	page := RunPage{Runs: runs}
	if len(runs) > int(limit) {
		page.Runs = runs[:limit]
		page.NextCursor = page.Runs[len(page.Runs)-1].ID
	}
	page.RemainingRuns = max(0, remaining-int64(len(page.Runs)))
	return page, nil
}
