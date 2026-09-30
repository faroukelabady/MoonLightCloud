package operations

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// OpsReader serves dashboard/CLI reads over incidents, deliveries,
// recoveries, and aggregate summaries.
type OpsReader struct {
	store   Store
	service *Service
	metrics *Metrics
	// offlineAfter bounds the transactional manual-resolve guard for
	// offline incidents. Unset means the guard is unwired: stateful
	// manual resolution fails closed rather than assuming clear.
	offlineAfter time.Duration
	guardWired   bool
}

func NewOpsReader(store Store, service *Service, metrics *Metrics) *OpsReader {
	return &OpsReader{store: store, service: service, metrics: metrics}
}

// SetManualGuard wires the offline-age threshold for the transactional
// manual-resolve guard.
func (r *OpsReader) SetManualGuard(offlineAfter time.Duration) {
	r.offlineAfter = offlineAfter
	r.guardWired = true
}

// Deliveries lists an incident's alert deliveries (recipient snapshots
// included for masking at the presentation layer only).
func (r *OpsReader) Deliveries(ctx context.Context, incidentID string) ([]Delivery, error) {
	if _, err := r.service.Get(ctx, incidentID); err != nil {
		return nil, err
	}
	return r.store.DeliveriesForIncident(ctx, incidentID)
}

// Recoveries lists an incident's recovery actions.
func (r *OpsReader) Recoveries(ctx context.Context, incidentID string) ([]Recovery, error) {
	if _, err := r.service.Get(ctx, incidentID); err != nil {
		return nil, err
	}
	return r.store.RecoveriesForIncident(ctx, incidentID)
}

// ResolveOperator resolves event incidents directly; stateful incidents
// resolve through a single predicate-guarded transaction (else
// CONDITION_STILL_ACTIVE), so a condition turning active mid-call
// cannot be falsely resolved.
func (r *OpsReader) ResolveOperator(ctx context.Context, id, code string) (Incident, error) {
	incident, err := r.service.Get(ctx, id)
	if err != nil {
		return Incident{}, err
	}
	if incident.State == StateResolved {
		return incident, nil
	}
	if code == "" {
		code = ResolutionOperator
	}
	if IsStateful(incident.Rule) {
		if !r.guardWired {
			// No guard threshold wired (e.g. engine disabled): fail
			// closed rather than assuming a clear condition.
			return Incident{}, apperr.New(apperr.Conflict, "CONDITION_STILL_ACTIVE")
		}
		now := time.Now().UTC()
		resolved, applied, err := r.store.ResolveStatefulIfClear(ctx, id, incident.Rule, incident.SubjectID, code, now.Add(-r.offlineAfter), now)
		if err != nil {
			return Incident{}, err
		}
		if !applied {
			current, cerr := r.service.Get(ctx, id)
			if cerr != nil {
				return Incident{}, cerr
			}
			if current.State == StateResolved {
				return current, nil
			}
			return Incident{}, apperr.New(apperr.Conflict, "CONDITION_STILL_ACTIVE")
		}
		return resolved, nil
	}
	return r.service.Resolve(ctx, id, code)
}

// Summary aggregates counters, open state, and device rollups.
func (r *OpsReader) Summary(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range r.metrics.Snapshot() {
		out[k] = v
	}
	devices, err := r.store.DeviceSummary(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		rows = append(rows, map[string]any{
			"device_id": d.DeviceID, "open_count": d.OpenCount, "max_severity": d.MaxSeverity,
		})
	}
	out["devices"] = rows
	return out, nil
}

// DeviceSummaries returns the batched per-device active-incident rollup
// (one aggregate query, never per-device).
func (r *OpsReader) DeviceSummaries(ctx context.Context) ([]map[string]any, error) {
	devices, err := r.store.DeviceSummary(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		rows = append(rows, map[string]any{
			"device_id": d.DeviceID, "open_count": d.OpenCount, "max_severity": d.MaxSeverity,
		})
	}
	return rows, nil
}
