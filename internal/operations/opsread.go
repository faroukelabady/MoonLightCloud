package operations

import (
	"context"
)

// OpsReader serves dashboard/CLI reads over incidents, deliveries,
// recoveries, and aggregate summaries.
type OpsReader struct {
	store   Store
	service *Service
	metrics *Metrics
	// stillActive re-evaluates stateful predicates for the manual-resolve
	// guard. Wired to the detector; nil means "condition assumed clear".
	stillActiveFn func(ctx context.Context, incident Incident) (bool, error)
}

func NewOpsReader(store Store, service *Service, metrics *Metrics) *OpsReader {
	return &OpsReader{store: store, service: service, metrics: metrics}
}

// SetStillActive wires the detector predicate after construction.
func (r *OpsReader) SetStillActive(fn func(ctx context.Context, incident Incident) (bool, error)) {
	r.stillActiveFn = fn
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
// require the condition to have cleared (else CONDITION_STILL_ACTIVE).
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
		still := false
		if r.stillActiveFn != nil {
			var err error
			still, err = r.stillActiveFn(ctx, incident)
			if err != nil {
				return Incident{}, err
			}
		}
		return r.service.ResolveIfClear(ctx, id, code, still)
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
