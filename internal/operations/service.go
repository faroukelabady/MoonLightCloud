package operations

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Service orchestrates incidents: open/adopt, acknowledge, resolve, and
// dashboard/CLI reads. Detector and workers build on these primitives.
type Service struct {
	store Store
	newID func() string
	now   func() time.Time
}

func NewService(store Store, newID func() string, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, newID: newID, now: now}
}

// OpenStateful opens or adopts the active incident for a stateful rule.
// Recurrence after true resolution creates a new episode.
func (s *Service) OpenStateful(ctx context.Context, rule, subjectType, subjectID, sourceKey string) (Incident, bool, error) {
	if err := ValidateRule(rule); err != nil {
		return Incident{}, false, err
	}
	now := s.now().UTC()
	episode, err := s.store.MaxEpisode(ctx, rule, subjectType, subjectID)
	if err != nil {
		return Incident{}, false, err
	}
	incident, created, err := s.store.OpenStateful(ctx, s.newID(), rule, subjectType, subjectID, SeverityFor(rule), sourceKey, episode+1, now)
	if err != nil {
		return Incident{}, false, err
	}
	if !created {
		_ = s.store.TouchObserved(ctx, incident.ID, now)
		incident.LastObservedAt = now
	}
	return incident, created, nil
}

// OpenEvent opens or adopts the all-time terminal-event incident.
func (s *Service) OpenEvent(ctx context.Context, rule, subjectType, subjectID, sourceKey string) (Incident, bool, error) {
	if err := ValidateRule(rule); err != nil {
		return Incident{}, false, err
	}
	if sourceKey == "" {
		return Incident{}, false, apperr.New(apperr.InvalidInput, "event incident requires a source key")
	}
	return s.store.OpenEvent(ctx, s.newID(), rule, subjectType, subjectID, SeverityFor(rule), sourceKey, s.now().UTC())
}

// Acknowledge is idempotent and never resolves.
func (s *Service) Acknowledge(ctx context.Context, id string) (Incident, error) {
	incident, ok, err := s.store.Acknowledge(ctx, id, s.now().UTC())
	if err != nil {
		return Incident{}, err
	}
	if !ok {
		return s.byID(ctx, id)
	}
	return incident, nil
}

// Resolve commits terminal resolution for events, or operator resolution
// when the caller already verified the stateful predicate cleared.
func (s *Service) Resolve(ctx context.Context, id, code string) (Incident, error) {
	if code == "" {
		return Incident{}, apperr.New(apperr.InvalidInput, "resolution code required")
	}
	incident, ok, err := s.store.Resolve(ctx, id, code, s.now().UTC())
	if err != nil {
		return Incident{}, err
	}
	if !ok {
		return s.byID(ctx, id)
	}
	return incident, nil
}

// ResolveIfClear resolves a stateful incident only when its predicate no
// longer holds; otherwise 409 CONDITION_STILL_ACTIVE.
func (s *Service) ResolveIfClear(ctx context.Context, id, code string, stillActive bool) (Incident, error) {
	if stillActive {
		return Incident{}, apperr.New(apperr.Conflict, "CONDITION_STILL_ACTIVE")
	}
	return s.Resolve(ctx, id, code)
}

func (s *Service) byID(ctx context.Context, id string) (Incident, error) {
	incident, ok, err := s.store.IncidentByID(ctx, id)
	if err != nil {
		return Incident{}, err
	}
	if !ok {
		return Incident{}, apperr.New(apperr.NotFound, "incident not found")
	}
	return incident, nil
}

// Get loads one incident for dashboard/CLI detail.
func (s *Service) Get(ctx context.Context, id string) (Incident, error) {
	return s.byID(ctx, id)
}

// List returns one bounded page plus the next cursor ("" when final).
// An empty cursor starts from the newest incident (keyset on opened_at).
func (s *Service) List(ctx context.Context, state, severity, rule, cursor string, limit int) ([]Incident, string, error) {
	if state != "" && state != StateOpen && state != StateAcknowledged && state != StateResolved {
		return nil, "", apperr.New(apperr.InvalidInput, "invalid incident state")
	}
	if severity != "" && severity != SeverityWarning && severity != SeverityUrgent {
		return nil, "", apperr.New(apperr.InvalidInput, "invalid incident severity")
	}
	if rule != "" {
		if err := ValidateRule(rule); err != nil {
			return nil, "", err
		}
	}
	var at *time.Time
	id := ""
	if cursor != "" {
		decodedAt, decodedID, err := DecodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		at, id = &decodedAt, decodedID
	}
	// Fetch one extra row to detect the next page without COUNT scans.
	rows, err := s.store.ListPage(ctx, state, severity, rule, at, id, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = EncodeCursor(last.OpenedAt, last.ID)
	}
	return rows, next, nil
}
