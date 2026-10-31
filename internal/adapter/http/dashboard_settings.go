package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/telegram"
	"github.com/faroukelabady/MoonLightCloud/internal/settings"
)

const maxSettingsBody = 8 << 10

// MessagingProvider is one configured notification provider. Credentials
// stay in the server environment; the dashboard only sees key and kind.
type MessagingProvider struct {
	Key  string `json:"key"`
	Kind string `json:"kind"` // "whatsapp" | "telegram"
}

// TemplateStore reads and writes provider template mappings.
type TemplateStore interface {
	UpsertTemplateMapping(ctx context.Context, mapping notifications.TemplateMapping) error
	ListTemplateMappings(ctx context.Context, providerKey string) ([]notifications.TemplateMapping, error)
}

// SettingsHandlers serve the dashboard Settings page: business toggles,
// exchange rates, and business-report delivery over WhatsApp/Telegram.
type SettingsHandlers struct {
	Settings settings.Service
	// Rates fetches the USD->EGP rate; nil when EXCHANGE_RATE_URL is empty.
	Rates            *settings.Fetcher
	Reports          *businessreports.Service
	Templates        TemplateStore
	Providers        []MessagingProvider
	SchedulerEnabled bool
}

func actorOf(r *http.Request) string {
	p, _ := PrincipalOf(r)
	return p.UserID
}

// Business serves GET /api/v1/dashboard/settings: the toggles and the
// rate history, readable by every report reader.
func (h *SettingsHandlers) Business(w http.ResponseWriter, r *http.Request) {
	business, err := h.Settings.Business(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	rates, err := h.Settings.Rates(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"business": business, "exchange_rates": rates, "rate_fetch": rateFetchWire{
		Enabled: h.Rates.Enabled(), Source: h.Rates.Source(), IntervalHours: int(settings.FetchInterval.Hours()),
	}})
}

// UpdateBusiness serves PUT /api/v1/dashboard/settings/business.
func (h *SettingsHandlers) UpdateBusiness(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaxEnabled      bool  `json:"tax_enabled"`
		DiscountEnabled bool  `json:"discount_enabled"`
		ReturnsEnabled  bool  `json:"returns_enabled"`
		Revision        int64 `json:"revision"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	saved, err := h.Settings.UpdateBusiness(r.Context(), settings.Business{
		TaxEnabled: req.TaxEnabled, DiscountEnabled: req.DiscountEnabled, ReturnsEnabled: req.ReturnsEnabled,
	}, req.Revision, actorOf(r))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// AddRate serves POST /api/v1/dashboard/settings/exchange-rates.
func (h *SettingsHandlers) AddRate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rate        string `json:"rate"`
		EffectiveAt string `json:"effective_at"`
		Note        string `json:"note"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	var at time.Time
	if strings.TrimSpace(req.EffectiveAt) != "" {
		parsed, err := time.Parse(time.RFC3339, req.EffectiveAt)
		if err != nil {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "effective_at must be an RFC 3339 time"))
			return
		}
		at = parsed
	}
	rate, err := h.Settings.AddRate(r.Context(), req.Rate, at, req.Note, actorOf(r))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, rate)
}

// rateFetchWire tells the Settings page whether Cloud fetches the rate.
type rateFetchWire struct {
	Enabled       bool   `json:"enabled"`
	Source        string `json:"source"`
	IntervalHours int    `json:"interval_hours"`
}

// FetchRate serves POST /api/v1/dashboard/settings/exchange-rates/fetch:
// fetch the provider rate now. 409 RATE_FETCH_DISABLED when fetching is
// off; 503 RATE_PROVIDER_FAILED keeps the stored rate.
func (h *SettingsHandlers) FetchRate(w http.ResponseWriter, r *http.Request) {
	res, err := h.Rates.RefreshNow(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// DeleteRate serves DELETE /api/v1/dashboard/settings/exchange-rates/{id}.
func (h *SettingsHandlers) DeleteRate(w http.ResponseWriter, r *http.Request) {
	if err := h.Settings.DeleteRate(r.Context(), r.PathValue("id")); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

type recipientWire struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	ProviderKey string `json:"provider_key"`
	// Recipient is masked: the dashboard never reads full phone numbers
	// or chat IDs back.
	Recipient string `json:"recipient_masked"`
	Locale    string `json:"locale"`
	Enabled   bool   `json:"enabled"`
}

type templateWire struct {
	TemplateKey  string   `json:"template_key"`
	Locale       string   `json:"locale"`
	ExternalName string   `json:"external_name"`
	Language     string   `json:"language"`
	Params       []string `json:"params"`
	Enabled      bool     `json:"enabled"`
}

type scheduleWire struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	LocalTime    string    `json:"local_time"`
	AnchorDate   string    `json:"anchor_local_date"`
	Enabled      bool      `json:"enabled"`
	NextRunAt    time.Time `json:"next_run_at"`
	RecipientIDs []string  `json:"recipient_ids"`
}

type runWire struct {
	ID            string    `json:"id"`
	ScheduleID    string    `json:"schedule_id"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	ScheduledFor  time.Time `json:"scheduled_for"`
	PeriodStart   time.Time `json:"period_start"`
	PeriodEnd     time.Time `json:"period_end"`
	AttemptCount  int32     `json:"attempt_count"`
	LastErrorCode string    `json:"last_error_code"`
}

func maskRecipient(value string) string {
	if len(value) <= 4 {
		return "…"
	}
	return "…" + value[len(value)-4:]
}

// Messaging serves GET /api/v1/dashboard/settings/messaging: providers
// with their report templates, recipients, schedules and recent runs.
func (h *SettingsHandlers) Messaging(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	type providerWire struct {
		MessagingProvider
		Templates []templateWire `json:"templates"`
	}
	providers := make([]providerWire, 0, len(h.Providers))
	for _, p := range h.Providers {
		mappings, err := h.Templates.ListTemplateMappings(ctx, p.Key)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		templates := []templateWire{}
		for _, m := range mappings {
			if m.TemplateKey != businessreports.TemplateDaily && m.TemplateKey != businessreports.TemplateTenDay {
				continue
			}
			templates = append(templates, templateWire{TemplateKey: m.TemplateKey, Locale: m.Locale,
				ExternalName: m.ExternalTemplateName, Language: m.ExternalLanguageCode,
				Params: append([]string{}, m.ParameterNames...), Enabled: m.Enabled})
		}
		providers = append(providers, providerWire{MessagingProvider: p, Templates: templates})
	}
	recipients, err := h.Reports.ListRecipients(ctx)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	recipientRows := make([]recipientWire, 0, len(recipients))
	for _, rc := range recipients {
		recipientRows = append(recipientRows, recipientWire{ID: rc.ID, Label: rc.Label, ProviderKey: rc.ProviderKey,
			Recipient: maskRecipient(rc.Recipient), Locale: rc.Locale, Enabled: rc.Enabled})
	}
	schedules, err := h.Reports.ListSchedules(ctx)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	scheduleRows := make([]scheduleWire, 0, len(schedules))
	for _, s := range schedules {
		linked, err := h.Reports.ScheduleRecipients(ctx, s.ID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		ids := make([]string, 0, len(linked))
		for _, rc := range linked {
			ids = append(ids, rc.ID)
		}
		scheduleRows = append(scheduleRows, scheduleWire{ID: s.ID, Name: s.Name, Kind: string(s.Kind),
			LocalTime: s.LocalTime, AnchorDate: s.AnchorLocalDate, Enabled: s.Enabled, NextRunAt: s.NextRunAt, RecipientIDs: ids})
	}
	runs, err := h.Reports.ListRuns(ctx, 20)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	runRows := make([]runWire, 0, len(runs))
	for _, run := range runs {
		runRows = append(runRows, runWire{ID: run.ID, ScheduleID: run.ScheduleID, Kind: string(run.Kind),
			Status: string(run.Status), ScheduledFor: run.ScheduledFor, PeriodStart: run.PeriodStart,
			PeriodEnd: run.PeriodEnd, AttemptCount: run.AttemptCount, LastErrorCode: run.LastErrorCode})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scheduler_enabled": h.SchedulerEnabled, "providers": providers,
		"recipients": recipientRows, "schedules": scheduleRows, "runs": runRows,
	})
}

func (h *SettingsHandlers) provider(key string) (MessagingProvider, bool) {
	for _, p := range h.Providers {
		if p.Key == key {
			return p, true
		}
	}
	return MessagingProvider{}, false
}

// AddRecipient serves POST /api/v1/dashboard/settings/messaging/recipients.
func (h *SettingsHandlers) AddRecipient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label       string `json:"label"`
		ProviderKey string `json:"provider_key"`
		Recipient   string `json:"recipient"`
		Locale      string `json:"locale"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	p, ok := h.provider(req.ProviderKey)
	if !ok {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "UNKNOWN_PROVIDER"))
		return
	}
	recipient := strings.TrimSpace(req.Recipient)
	// Each provider accepts only its own address form.
	if p.Kind == "whatsapp" {
		recipient = strings.TrimPrefix(strings.NewReplacer(" ", "", "-", "").Replace(recipient), "+")
		if err := notifications.ValidateWhatsAppRecipient(recipient); err != nil {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "INVALID_RECIPIENT"))
			return
		}
	} else if _, err := telegram.ValidateRecipient(recipient); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "INVALID_RECIPIENT"))
		return
	}
	created, err := h.Reports.AddRecipient(r.Context(), strings.TrimSpace(req.Label), p.Key, recipient, req.Locale)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, recipientWire{ID: created.ID, Label: created.Label, ProviderKey: created.ProviderKey,
		Recipient: maskRecipient(created.Recipient), Locale: created.Locale, Enabled: created.Enabled})
}

// SetRecipientEnabled serves POST .../recipients/{id}/enable and /disable.
func (h *SettingsHandlers) SetRecipientEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		set := h.Reports.DisableRecipient
		if enabled {
			set = h.Reports.EnableRecipient
		}
		if err := set(r.Context(), r.PathValue("id")); err != nil {
			WriteError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
	}
}

// AddSchedule serves POST /api/v1/dashboard/settings/messaging/schedules.
func (h *SettingsHandlers) AddSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string   `json:"name"`
		Kind         string   `json:"kind"`
		LocalTime    string   `json:"local_time"`
		AnchorDate   string   `json:"anchor_local_date"`
		RecipientIDs []string `json:"recipient_ids"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	s, err := h.Reports.CreateSchedule(r.Context(), strings.TrimSpace(req.Name), req.Kind, req.LocalTime, req.AnchorDate, req.RecipientIDs)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, scheduleWire{ID: s.ID, Name: s.Name, Kind: string(s.Kind), LocalTime: s.LocalTime,
		AnchorDate: s.AnchorLocalDate, Enabled: s.Enabled, NextRunAt: s.NextRunAt, RecipientIDs: req.RecipientIDs})
}

// SetScheduleEnabled serves POST .../schedules/{id}/enable and /disable.
func (h *SettingsHandlers) SetScheduleEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		set := h.Reports.DisableSchedule
		if enabled {
			set = h.Reports.EnableSchedule
		}
		if _, err := set(r.Context(), r.PathValue("id")); err != nil {
			WriteError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
	}
}

// RunSchedule serves POST .../schedules/{id}/run: send the report now.
// The client's request key makes a double click send only once.
func (h *SettingsHandlers) RunSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestKey string `json:"request_key"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	id, created, err := h.Reports.RunNow(r.Context(), r.PathValue("id"), "dashboard:"+req.RequestKey)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "created": created, "scheduler_enabled": h.SchedulerEnabled})
}

// SetTemplate serves PUT /api/v1/dashboard/settings/messaging/templates:
// which provider template carries each report in each language.
func (h *SettingsHandlers) SetTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderKey  string   `json:"provider_key"`
		TemplateKey  string   `json:"template_key"`
		Locale       string   `json:"locale"`
		ExternalName string   `json:"external_name"`
		Language     string   `json:"language"`
		Params       []string `json:"params"`
		Enabled      bool     `json:"enabled"`
	}
	if err := decodeStrictBody(r, maxSettingsBody, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	if _, ok := h.provider(req.ProviderKey); !ok {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "UNKNOWN_PROVIDER"))
		return
	}
	if req.TemplateKey != businessreports.TemplateDaily && req.TemplateKey != businessreports.TemplateTenDay {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "UNKNOWN_TEMPLATE"))
		return
	}
	if err := businessreports.ValidateLocale(req.Locale); err != nil {
		WriteError(w, r, err)
		return
	}
	name, language := strings.TrimSpace(req.ExternalName), strings.TrimSpace(req.Language)
	if name == "" || len(name) > 128 || language == "" || len(language) > 16 || len(req.Params) > 10 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "INVALID_TEMPLATE"))
		return
	}
	params := make([]string, 0, len(req.Params))
	for _, p := range req.Params {
		if p = strings.TrimSpace(p); p == "" || len(p) > 64 {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "INVALID_TEMPLATE"))
			return
		}
		params = append(params, p)
	}
	if err := h.Templates.UpsertTemplateMapping(r.Context(), notifications.TemplateMapping{
		ProviderKey: req.ProviderKey, TemplateKey: req.TemplateKey, Locale: req.Locale,
		ExternalTemplateName: name, ExternalLanguageCode: language, ParameterNames: params, Enabled: req.Enabled,
	}); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, templateWire{TemplateKey: req.TemplateKey, Locale: req.Locale,
		ExternalName: name, Language: language, Params: params, Enabled: req.Enabled})
}
