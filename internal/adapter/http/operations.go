package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
)

// OperationsHandlers serves operator incident endpoints (dashboard
// session auth only). Incident bodies carry bounded machine summaries;
// full alert recipients never appear here.
type OperationsHandlers struct {
	Svc *operations.Service
	Ops *operations.OpsReader
}

type incidentWire struct {
	ID             string  `json:"id"`
	Rule           string  `json:"rule"`
	SubjectType    string  `json:"subject_type"`
	SubjectID      string  `json:"subject_id"`
	Severity       string  `json:"severity"`
	State          string  `json:"state"`
	Episode        int     `json:"episode"`
	OpenedAt       string  `json:"opened_at"`
	LastObservedAt string  `json:"last_observed_at"`
	AcknowledgedAt *string `json:"acknowledged_at,omitempty"`
	ResolvedAt     *string `json:"resolved_at,omitempty"`
	ResolutionCode *string `json:"resolution_code,omitempty"`
}

func toIncidentWire(in operations.Incident) incidentWire {
	w := incidentWire{
		ID: in.ID, Rule: in.Rule, SubjectType: in.SubjectType, SubjectID: in.SubjectID,
		Severity: in.Severity, State: in.State, Episode: in.Episode,
		OpenedAt:       in.OpenedAt.UTC().Format(time.RFC3339Nano),
		LastObservedAt: in.LastObservedAt.UTC().Format(time.RFC3339Nano),
	}
	if in.AcknowledgedAt != nil {
		s := in.AcknowledgedAt.UTC().Format(time.RFC3339Nano)
		w.AcknowledgedAt = &s
	}
	if in.ResolvedAt != nil {
		s := in.ResolvedAt.UTC().Format(time.RFC3339Nano)
		w.ResolvedAt = &s
	}
	if in.ResolutionCode != nil {
		w.ResolutionCode = in.ResolutionCode
	}
	return w
}

// Incidents lists one bounded page with state/severity/rule filters.
func (h *OperationsHandlers) Incidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	q := r.URL.Query()
	limit := 20
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 100 {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid page limit"))
			return
		}
		limit = n
	}
	rows, next, err := h.Svc.List(r.Context(),
		strings.TrimSpace(q.Get("state")), strings.TrimSpace(q.Get("severity")),
		strings.TrimSpace(q.Get("rule")), strings.TrimSpace(q.Get("cursor")), limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	out := make([]incidentWire, 0, len(rows))
	for _, in := range rows {
		out = append(out, toIncidentWire(in))
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": out, "next_cursor": next})
}

// IncidentDetail returns one incident with deliveries and recovery state.
func (h *OperationsHandlers) IncidentDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	id := opsIncidentID(r.URL.Path)
	if id == "" {
		WriteError(w, r, apperr.New(apperr.NotFound, "incident not found"))
		return
	}
	incident, err := h.Svc.Get(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	deliveries, err := h.Ops.Deliveries(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	recoveries, err := h.Ops.Recoveries(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	dels := make([]deliveryWire, 0, len(deliveries))
	for _, dl := range deliveries {
		dels = append(dels, deliveryWire{
			ID: dl.ID, Event: dl.Event, ProviderKey: dl.ProviderKey,
			RecipientMasked: maskOpsRecipient(dl.RecipientSnapshot),
			Locale:          dl.Locale, TemplateKey: dl.TemplateKey, Status: dl.Status,
			LastErrorCode: dl.LastErrorCode, NotificationID: dl.NotificationID,
			NotificationDispatch: dl.NotificationDispatch,
			NotificationDelivery: dl.NotificationDelivery,
			HasNotification:      dl.NotificationID != nil,
		})
	}
	recs := make([]recoveryWire, 0, len(recoveries))
	for _, rc := range recoveries {
		recs = append(recs, recoveryWire{
			ID: rc.ID, ActionType: rc.ActionType, State: rc.State,
			TargetEntityID: rc.TargetEntityID, ResultCode: rc.ResultCode,
			AttemptCount: rc.AttemptCount, LastErrorCode: rc.LastErrorCode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"incident":   toIncidentWire(incident),
		"deliveries": dels,
		"recoveries": recs,
	})
}

// Acknowledge marks open → acknowledged (idempotent).
func (h *OperationsHandlers) Acknowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	id := opsIncidentActionID(r.URL.Path, "/acknowledge")
	if id == "" {
		WriteError(w, r, apperr.New(apperr.NotFound, "incident not found"))
		return
	}
	incident, err := h.Svc.Acknowledge(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incident": toIncidentWire(incident)})
}

// Resolve closes event incidents, or stateful ones whose condition
// cleared (else 409 CONDITION_STILL_ACTIVE).
func (h *OperationsHandlers) Resolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	id := opsIncidentActionID(r.URL.Path, "/resolve")
	if id == "" {
		WriteError(w, r, apperr.New(apperr.NotFound, "incident not found"))
		return
	}
	var body struct {
		ResolutionCode string `json:"resolution_code"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		if err := dec.Decode(&body); err != nil {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid resolve request"))
			return
		}
	}
	incident, err := h.Ops.ResolveOperator(r.Context(), id, strings.TrimSpace(body.ResolutionCode))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incident": toIncidentWire(incident)})
}

// Summary exposes aggregate counters and the batched device summary.
func (h *OperationsHandlers) Summary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	summary, err := h.Ops.Summary(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func opsIncidentID(path string) string {
	const prefix = "/api/v1/dashboard/operations/incidents/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" || strings.Contains(rest, "/") || len(rest) > 64 {
		return ""
	}
	return rest
}

func opsIncidentActionID(path, suffix string) string {
	const prefix = "/api/v1/dashboard/operations/incidents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	id := strings.Trim(strings.TrimSuffix(rest, strings.Trim(suffix, "/")), "/")
	if id == "" || strings.Contains(id, "/") || len(id) > 64 {
		return ""
	}
	return id
}

type deliveryWire struct {
	ID                   string  `json:"id"`
	Event                string  `json:"event"`
	ProviderKey          string  `json:"provider_key"`
	RecipientMasked      string  `json:"recipient_masked"`
	Locale               string  `json:"locale"`
	TemplateKey          string  `json:"template_key"`
	Status               string  `json:"status"`
	LastErrorCode        *string `json:"last_error_code,omitempty"`
	NotificationID       *string `json:"notification_id,omitempty"`
	NotificationDispatch *string `json:"notification_dispatch,omitempty"`
	NotificationDelivery *string `json:"notification_delivery,omitempty"`
	HasNotification      bool    `json:"has_notification"`
}

func maskOpsRecipient(recipient string) string {
	if len(recipient) <= 4 {
		return "..."
	}
	return "..." + recipient[len(recipient)-4:]
}

type recoveryWire struct {
	ID             string  `json:"id"`
	ActionType     string  `json:"action_type"`
	State          string  `json:"state"`
	TargetEntityID *string `json:"target_entity_id,omitempty"`
	ResultCode     *string `json:"result_code,omitempty"`
	AttemptCount   int     `json:"attempt_count"`
	LastErrorCode  *string `json:"last_error_code,omitempty"`
}

// DeviceSummary returns active incident counts per device in one batched
// aggregate read (never per-device queries).
func (h *OperationsHandlers) DeviceSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	rows, err := h.Ops.DeviceSummaries(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": rows})
}
