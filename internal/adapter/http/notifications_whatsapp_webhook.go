package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// WhatsApp webhook body bound: oversized callbacks are rejected before
// any signature processing or persistence.
const whatsAppWebhookMaxBodyBytes = 1 << 20

// NotificationStatusStore is the durable delivery-status boundary for
// WhatsApp callbacks. Correlation uses provider message IDs only:
// recipient data never keys a lookup.
type NotificationStatusStore interface {
	LookupByProviderMessage(ctx context.Context, providerKey, providerMessageID string) (string, bool, error)
	ApplyDeliveryStatus(ctx context.Context, notificationID string, event notifications.DeliveryEvent) (notifications.DeliveryOutcome, error)
}

// WhatsAppWebhookHandlers serves Meta webhook callbacks. GET carries
// the verification token flow; POST carries HMAC-signed status
// callbacks. The routes are public-but-signed: no dashboard session,
// no device token, no report token is accepted. Authority comes solely
// from the verify token (GET) or the app-secret HMAC (POST).
type WhatsAppWebhookHandlers struct {
	store              NotificationStatusStore
	resolveVerifyToken func(providerKey string) (string, bool)
	resolveAppSecret   func(providerKey string) (string, bool)
	log                *slog.Logger
}

// NewWhatsAppWebhookHandlers wires callback handling over the delivery
// store. Resolvers map a path provider key to its configured secrets
// (per-provider isolation for future multi-instance support).
func NewWhatsAppWebhookHandlers(store NotificationStatusStore,
	resolveVerifyToken func(string) (string, bool),
	resolveAppSecret func(string) (string, bool),
	log *slog.Logger) *WhatsAppWebhookHandlers {
	return &WhatsAppWebhookHandlers{
		store: store, resolveVerifyToken: resolveVerifyToken,
		resolveAppSecret: resolveAppSecret, log: log,
	}
}

// VerifyWhatsAppWebhook serves GET verification: hub.mode=subscribe
// plus a constant-time verify-token match returns the bounded
// challenge. Wrong mode, missing token, or token mismatch is
// rejected; configured token values never appear in responses or logs.
func (h *WhatsAppWebhookHandlers) VerifyWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	providerKey := r.PathValue("provider_key")
	token, ok := h.resolveVerifyToken(providerKey)
	if !ok || token == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown provider"})
		return
	}
	query := r.URL.Query()
	if query.Get("hub.mode") != "subscribe" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported verification mode"})
		return
	}
	if !verifyTokenEqual(query.Get("hub.verify_token"), token) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid verify token"})
		return
	}
	challenge := query.Get("hub.challenge")
	if !printableRange(challenge, 1, 128) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid challenge"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(challenge))
}

// verifyTokenEqual compares verification tokens without leaking length
// or content through timing.
func verifyTokenEqual(supplied, configured string) bool {
	if supplied == "" || configured == "" {
		return false
	}
	left := sha256.Sum256([]byte(supplied))
	right := sha256.Sum256([]byte(configured))
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

// printableRange accepts short printable ASCII without control
// characters; webhook query values travel in URLs and must stay inert.
func printableRange(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 32 || value[i] == 127 {
			return false
		}
	}
	return true
}

// whatsAppStatusEntry is one outbound status callback. Timestamp is
// Unix seconds per current Meta behavior, decoded tolerantly.
type whatsAppStatusEntry struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Timestamp   json.Number     `json:"timestamp"`
	RecipientID string          `json:"recipient_id"`
	Errors      []whatsAppError `json:"errors"`
}

// whatsAppError is one provider failure detail. Only the bounded
// numeric code is ever persisted.
type whatsAppError struct {
	Code json.Number `json:"code"`
}

// StatusWhatsAppWebhook serves POST status callbacks: raw-body HMAC
// verification, bounded parsing, then atomic history/current updates
// per known provider message ID. Inbound customer content and unknown
// IDs are acknowledged without persistence.
func (h *WhatsAppWebhookHandlers) StatusWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	providerKey := r.PathValue("provider_key")
	secret, ok := h.resolveAppSecret(providerKey)
	if !ok || secret == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown provider"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, whatsAppWebhookMaxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unreadable body"})
		return
	}
	if int64(len(raw)) > whatsAppWebhookMaxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "body too large"})
		return
	}
	if !verifyHubSignature(raw, r.Header.Get("X-Hub-Signature-256"), secret) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid webhook signature"})
		return
	}
	var envelope struct {
		Object string `json:"object"`
		Entry  []struct {
			Changes []struct {
				Field string `json:"field"`
				Value struct {
					Statuses []whatsAppStatusEntry `json:"statuses"`
					Messages []json.RawMessage     `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid webhook body"})
		return
	}
	if envelope.Object != "" && envelope.Object != "whatsapp_business_account" {
		// Not our object: acknowledge without work or retry loops.
		writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
		return
	}
	for _, entry := range envelope.Entry {
		for _, change := range entry.Changes {
			if change.Field != "" && change.Field != "messages" {
				continue
			}
			// Inbound customer messages share this field: acknowledged,
			// never persisted, never logged beyond counts.
			for _, status := range change.Value.Statuses {
				if !h.applyStatus(r.Context(), providerKey, status) {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "status persistence failed"})
					return
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
}

// verifyHubSignature checks sha256=<hex-HMAC-SHA256(appSecret,
// rawBody)> with constant-time comparison over the exact raw bytes.
// Malformed encodings fail closed without persistence.
func verifyHubSignature(raw []byte, header, secret string) bool {
	const prefix = "sha256="
	trimmed := strings.TrimSpace(header)
	if !strings.HasPrefix(trimmed, prefix) {
		return false
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(trimmed, prefix))
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return hmac.Equal(mac.Sum(nil), signature)
}

// applyStatus normalizes one status entry and persists it. Unknown
// provider message IDs are ignored without writes; malformed
// timestamps record history without advancing current state. Only safe
// operational fields are logged: never recipient, never error prose.
func (h *WhatsAppWebhookHandlers) applyStatus(ctx context.Context, providerKey string, status whatsAppStatusEntry) bool {
	if !printableRange(status.ID, 1, 256) {
		return true
	}
	canonical := notifications.MapProviderStatus(status.Status)
	event := notifications.DeliveryEvent{
		ProviderMessageID: status.ID,
		RawStatus:         boundStatusRaw(status.Status),
		Canonical:         canonical,
		ProviderTimestamp: parseStatusTimestamp(status.Timestamp.String()),
		ErrorCode:         statusErrorCode(status.Errors),
	}
	event.Fingerprint = notifications.DeliveryEventFingerprint(
		providerKey, event.ProviderMessageID, event.RawStatus,
		event.Canonical, event.ProviderTimestamp, event.ErrorCode)
	notificationID, found, err := h.store.LookupByProviderMessage(ctx, providerKey, status.ID)
	if err != nil {
		h.logError("notification status lookup failed", "provider", providerKey, "err", err.Error())
		return false
	}
	if !found {
		// Another system's message: acknowledge, persist nothing.
		return true
	}
	if _, err := h.store.ApplyDeliveryStatus(ctx, notificationID, event); err != nil {
		h.logError("notification status apply failed",
			"provider", providerKey, "notification", notificationID, "err", err.Error())
		return false
	}
	h.logInfo("notification status applied",
		"provider", providerKey, "notification", notificationID,
		"status", string(canonical))
	return true
}

// boundStatusRaw keeps the raw provider status inert and bounded for
// history preservation.
func boundStatusRaw(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "empty"
	}
	var bounded strings.Builder
	for i := 0; i < len(trimmed) && bounded.Len() < 64; i++ {
		c := trimmed[i]
		if c < 32 || c == 127 {
			continue
		}
		bounded.WriteByte(c)
	}
	return bounded.String()
}

// parseStatusTimestamp decodes Meta Unix-seconds timestamps. Impossible
// values yield nil: history without current-state advancement.
func parseStatusTimestamp(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	seconds, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || seconds <= 0 || seconds > 4102444800 {
		return nil
	}
	at := time.Unix(seconds, 0).UTC()
	return &at
}

// statusErrorCode keeps only the first bounded numeric provider error
// code. Titles, details, and recipients are never persisted.
func statusErrorCode(details []whatsAppError) string {
	if len(details) == 0 {
		return ""
	}
	code := strings.TrimSpace(details[0].Code.String())
	if len(code) > 64 {
		code = code[:64]
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 32 || code[i] == 127 {
			return ""
		}
	}
	return code
}

func (h *WhatsAppWebhookHandlers) logInfo(msg string, args ...any) {
	if h.log != nil {
		h.log.Info(msg, args...)
	}
}

func (h *WhatsAppWebhookHandlers) logError(msg string, args ...any) {
	if h.log != nil {
		h.log.Error(msg, args...)
	}
}
