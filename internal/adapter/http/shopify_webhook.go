package http

import (
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/shopify"
)

// shopifyWebhookMaxBodyBytes bounds Shopify webhook deliveries before
// signature processing or persistence (repository-consistent 1 MiB
// order-webhook bound).
const shopifyWebhookMaxBodyBytes = 1 << 20

// ShopifyWebhookConfig is the per-provider webhook authority: the app
// client secret (HMAC key) and the configured shop domain. Both are
// runtime-only and never persisted or echoed.
type ShopifyWebhookConfig struct {
	ClientSecret string
	ShopDomain   string
}

// ShopifyWebhookHandlers serves signed Shopify order webhooks at
// POST /api/v1/commerce/webhooks/shopify/{provider_key}. The route is
// public-but-signed: authority comes solely from the raw-body HMAC.
// Delivery flow is the frozen durable-ingress shape: authenticate,
// validate the minimal envelope, durably record/adopt the delivery,
// respond, process asynchronously.
type ShopifyWebhookHandlers struct {
	store         WebhookInboxStore
	resolveConfig func(providerKey string) (ShopifyWebhookConfig, bool)
	notify        func()
	log           *slog.Logger
}

// NewShopifyWebhookHandlers wires Shopify webhook ingestion over the
// same generic inbox store the WooCommerce path uses. resolveConfig
// maps a path provider key to its configured Shopify webhook authority
// (per-provider isolation); notify wakes the shared reconciliation
// processor.
func NewShopifyWebhookHandlers(store WebhookInboxStore, resolveConfig func(string) (ShopifyWebhookConfig, bool), notify func(), log *slog.Logger) *ShopifyWebhookHandlers {
	return &ShopifyWebhookHandlers{store: store, resolveConfig: resolveConfig, notify: notify, log: log}
}

// ShopifyWebhook serves one Shopify order webhook delivery.
func (h *ShopifyWebhookHandlers) ShopifyWebhook(w http.ResponseWriter, r *http.Request) {
	providerKey := r.PathValue("provider_key")
	cfg, ok := h.resolveConfig(providerKey)
	if !ok || cfg.ClientSecret == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown provider"})
		return
	}
	raw, ok := h.readBoundedBody(w, r)
	if !ok {
		return
	}
	// HMAC over the exact raw bytes, before any dedupe: an
	// invalid-signature duplicate is never accepted merely because its
	// delivery id already exists.
	if !verifyWebhookSignature(raw, r.Header.Get("X-Shopify-Hmac-Sha256"), cfg.ClientSecret) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid webhook signature"})
		return
	}
	// A correctly signed webhook for another Shopify shop must not
	// enter this provider instance.
	shopDomain := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Shopify-Shop-Domain")))
	if shopDomain == "" || shopDomain != cfg.ShopDomain {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid shop domain"})
		return
	}
	// Topic comes from the header (never the body) and must be in the
	// supported order topic set.
	topic, err := shopify.ParseWebhookTopic(r.Header.Get("X-Shopify-Topic"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported webhook topic"})
		return
	}
	deliveryID := strings.TrimSpace(r.Header.Get("X-Shopify-Webhook-Id"))
	if err := orders.ValidateDeliveryID(deliveryID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid delivery identity"})
		return
	}
	externalOrderID, ok := shopify.WebhookOrderID(raw)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid order identity"})
		return
	}
	canonicalID, err := orders.CanonicalExternalOrderID(externalOrderID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid order identity"})
		return
	}
	// Minimal durable metadata only: the raw body is hashed, never
	// persisted. No customer PII ever reaches the inbox.
	sum := sha256.Sum256(raw)
	outcome, err := h.store.InsertOrderWebhookEvent(r.Context(),
		commerce.ProviderKey(providerKey), deliveryID, topic, canonicalID, sum[:], nil)
	if err != nil {
		var appErr *apperr.Error
		if errors.As(err, &appErr) && appErr.Kind == apperr.Conflict {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "delivery already recorded"})
			return
		}
		WriteError(w, r, err)
		return
	}
	if outcome == orders.WebhookInserted && h.notify != nil {
		h.notify()
	}
	if h.log != nil {
		h.log.Info("shopify webhook accepted",
			"request_id", RequestID(r), "provider", providerKey,
			"delivery", deliveryID, "topic", string(topic), "order", canonicalID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}

// readBoundedBody reads the exact raw bytes for HMAC verification with
// a hard ceiling. Oversize means 413 with zero DB writes.
func (h *ShopifyWebhookHandlers) readBoundedBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, shopifyWebhookMaxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unreadable body"})
		return nil, false
	}
	if int64(len(raw)) > shopifyWebhookMaxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "body too large"})
		return nil, false
	}
	return raw, true
}

// Shopify deliveries use the shared raw-body HMAC verification helper
// (base64(HMAC-SHA256(clientSecret, rawBody)), constant-time comparison
// over the exact raw bytes, malformed encodings fail closed).
