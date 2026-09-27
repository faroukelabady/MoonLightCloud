package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// Webhook body bound: oversized deliveries are rejected before any
// signature processing or persistence.
const commerceWebhookMaxBodyBytes = 1 << 20

// WebhookInboxStore is the durable delivery boundary for webhook ingestion.
type WebhookInboxStore interface {
	InsertOrderWebhookEvent(ctx context.Context, providerKey commerce.ProviderKey, deliveryID string, topic orders.WebhookTopic, externalOrderID string, payloadHash []byte, webhookID *string) (bool, error)
}

// CommerceWebhookHandlers serves signed provider webhooks. The route is
// public-but-signed: no dashboard session, no device token, no report
// token is accepted. Authority comes solely from the HMAC signature.
type CommerceWebhookHandlers struct {
	store         WebhookInboxStore
	resolveSecret func(providerKey string) (string, bool)
	notify        func()
	log           *slog.Logger
}

// NewCommerceWebhookHandlers wires webhook ingestion over the inbox
// store. resolveSecret maps a path provider key to its configured
// webhook secret (per-provider isolation for future multi-instance
// support); notify wakes the reconciliation processor.
func NewCommerceWebhookHandlers(store WebhookInboxStore, resolveSecret func(string) (string, bool), notify func(), log *slog.Logger) *CommerceWebhookHandlers {
	return &CommerceWebhookHandlers{store: store, resolveSecret: resolveSecret, notify: notify, log: log}
}

// WooCommerceWebhook serves POST /api/v1/commerce/webhooks/woocommerce/{provider_key}.
func (h *CommerceWebhookHandlers) WooCommerceWebhook(w http.ResponseWriter, r *http.Request) {
	providerKey := r.PathValue("provider_key")
	secret, ok := h.resolveSecret(providerKey)
	if !ok || secret == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown provider"})
		return
	}
	raw, ok := h.readBoundedBody(w, r)
	if !ok {
		return
	}
	if !verifyWebhookSignature(raw, r.Header.Get("X-WC-Webhook-Signature"), secret) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid webhook signature"})
		return
	}
	if strings.TrimSpace(r.Header.Get("X-WC-Webhook-Resource")) != "order" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported webhook resource"})
		return
	}
	topic, err := orders.ParseWebhookTopic(r.Header.Get("X-WC-Webhook-Topic"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported webhook topic"})
		return
	}
	deliveryID := strings.TrimSpace(r.Header.Get("X-WC-Webhook-Delivery-ID"))
	if err := orders.ValidateDeliveryID(deliveryID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid delivery identity"})
		return
	}
	externalOrderID, ok := webhookOrderID(raw)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid order identity"})
		return
	}
	var webhookID *string
	if id := strings.TrimSpace(r.Header.Get("X-WC-Webhook-ID")); id != "" && len(id) <= 64 {
		webhookID = &id
	}
	sum := sha256.Sum256(raw)
	inserted, err := h.store.InsertOrderWebhookEvent(r.Context(),
		commerce.ProviderKey(providerKey), deliveryID, topic, externalOrderID, sum[:], webhookID)
	if err != nil {
		var appErr *apperr.Error
		if errors.As(err, &appErr) && appErr.Kind == apperr.Conflict {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "delivery already recorded"})
			return
		}
		WriteError(w, r, err)
		return
	}
	if inserted && h.notify != nil {
		h.notify()
	}
	if h.log != nil {
		h.log.Info("commerce webhook accepted",
			"request_id", RequestID(r), "provider", providerKey,
			"delivery", deliveryID, "topic", string(topic), "order", externalOrderID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}

// readBoundedBody reads the exact raw bytes for HMAC verification with
// a hard ceiling. Oversize means 413 with no persistence and no
// signature processing beyond what is safe.
func (h *CommerceWebhookHandlers) readBoundedBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, commerceWebhookMaxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unreadable body"})
		return nil, false
	}
	if int64(len(raw)) > commerceWebhookMaxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "body too large"})
		return nil, false
	}
	return raw, true
}

// verifyWebhookSignature checks base64(HMAC-SHA256(secret, rawBody))
// with constant-time comparison over the exact raw bytes. Malformed
// encodings fail closed without persistence.
func verifyWebhookSignature(raw []byte, header, secret string) bool {
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header))
	if err != nil || len(signature) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return hmac.Equal(mac.Sum(nil), signature)
}

// webhookOrderID extracts the canonical external order identity from
// the minimal signed JSON without retaining anything else.
func webhookOrderID(raw []byte) (string, bool) {
	var minimal struct {
		ID json.Number `json:"id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&minimal); err != nil {
		return "", false
	}
	canonical, err := orders.CanonicalExternalOrderID(minimal.ID.String())
	if err != nil {
		return "", false
	}
	return canonical, true
}
