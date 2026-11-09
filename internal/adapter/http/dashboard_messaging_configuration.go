package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/telegram"
)

func reportRecipientAddress(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if kind == "whatsapp" {
		value = strings.TrimPrefix(strings.NewReplacer(" ", "", "-", "").Replace(value), "+")
		if strings.HasPrefix(value, "0") {
			return "", apperr.New(apperr.InvalidInput, "WHATSAPP_COUNTRY_CODE_REQUIRED")
		}
		if err := notifications.ValidateWhatsAppRecipient(value); err != nil {
			return "", apperr.New(apperr.InvalidInput, "INVALID_RECIPIENT")
		}
	} else if _, err := telegram.ValidateRecipient(value); err != nil {
		return "", apperr.New(apperr.InvalidInput, "INVALID_RECIPIENT")
	}
	return value, nil
}

// Addresses remain masked in lists. Only this all-Stores settings editor
// can retrieve a full address, and it must never be cached.
func (h *SettingsHandlers) GetRecipient(w http.ResponseWriter, r *http.Request) {
	recipient, err := h.Reports.GetRecipient(r.Context(), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"id": recipient.ID, "label": recipient.Label, "provider_key": recipient.ProviderKey, "recipient": recipient.Recipient, "locale": recipient.Locale, "enabled": recipient.Enabled})
}

func (h *SettingsHandlers) UpdateRecipient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label     string `json:"label"`
		Recipient string `json:"recipient"`
		Locale    string `json:"locale"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	current, err := h.Reports.GetRecipient(r.Context(), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	provider, ok := h.provider(current.ProviderKey)
	if !ok {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "UNKNOWN_PROVIDER"))
		return
	}
	address, err := reportRecipientAddress(provider.Kind, req.Recipient)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	updated, err := h.Reports.UpdateRecipient(r.Context(), current.ID, req.Label, address, req.Locale)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recipientWire{ID: updated.ID, Label: updated.Label, ProviderKey: updated.ProviderKey, Recipient: maskRecipient(updated.Recipient), Locale: updated.Locale, Enabled: updated.Enabled})
}

func (h *SettingsHandlers) DeleteRecipient(w http.ResponseWriter, r *http.Request) {
	if err := h.Reports.DeleteRecipient(r.Context(), r.PathValue("id")); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *SettingsHandlers) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string   `json:"name"`
		Kind         string   `json:"kind"`
		LocalTime    string   `json:"local_time"`
		Anchor       string   `json:"anchor_local_date"`
		RecipientIDs []string `json:"recipient_ids"`
		Revision     int64    `json:"revision"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	s, err := h.Reports.UpdateSchedule(r.Context(), r.PathValue("id"), req.Name, req.Kind, req.LocalTime, req.Anchor, req.RecipientIDs, req.Revision)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scheduleWire{ID: s.ID, Name: s.Name, Kind: string(s.Kind), LocalTime: s.LocalTime, AnchorDate: s.AnchorLocalDate, Enabled: s.Enabled, NextRunAt: s.NextRunAt, RecipientIDs: req.RecipientIDs, Revision: s.Revision})
}

func (h *SettingsHandlers) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := h.Reports.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *SettingsHandlers) ReportRunPage(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 50 {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "page size must be 1..50"))
			return
		}
	}
	page, err := h.Reports.ListRunPage(r.Context(), r.URL.Query().Get("before"), int32(limit))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	rows := make([]runWire, 0, len(page.Runs))
	for _, run := range page.Runs {
		rows = append(rows, runWire{ID: run.ID, ScheduleID: run.ScheduleID, ScheduleName: run.ScheduleName, Kind: string(run.Kind), Status: string(run.Status), ScheduledFor: run.ScheduledFor, PeriodStart: run.PeriodStart, PeriodEnd: run.PeriodEnd, AttemptCount: run.AttemptCount, LastErrorCode: run.LastErrorCode, Deliveries: h.deliveryStates(run)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": rows, "next_cursor": page.NextCursor, "remaining_runs": page.RemainingRuns})
}
