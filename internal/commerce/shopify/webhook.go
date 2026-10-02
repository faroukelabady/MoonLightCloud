package shopify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// Shopify order webhook topics (Phase 11). Subscription setup is manual
// and documented in docs/operations/shopify.md; Cloud never registers
// subscriptions at startup (zero startup network dependency).
const (
	WebhookTopicOrderCreate    = "orders/create"
	WebhookTopicOrderUpdated   = "orders/updated"
	WebhookTopicOrderCancelled = "orders/cancelled"
	WebhookTopicOrderDelete    = "orders/delete"
)

// ParseWebhookTopic validates the X-Shopify-Topic header value against
// the supported topic set and maps it onto the frozen generic order
// topics. Cancellation is an order state change, not a deletion: it
// reconciles the authoritative current snapshot (which carries the
// cancelled state) through the update path.
func ParseWebhookTopic(value string) (orders.WebhookTopic, error) {
	switch strings.TrimSpace(value) {
	case WebhookTopicOrderCreate:
		return orders.TopicOrderCreated, nil
	case WebhookTopicOrderUpdated, WebhookTopicOrderCancelled:
		return orders.TopicOrderUpdated, nil
	case WebhookTopicOrderDelete:
		return orders.TopicOrderDeleted, nil
	default:
		return "", fmt.Errorf("unsupported webhook topic %q", value)
	}
}

// WebhookOrderID extracts the canonical external order identity from a
// signed Shopify order webhook payload without retaining anything else.
//
// Identity normalization (ADR-0044): when the payload exposes the
// GraphQL Admin ID (admin_graphql_api_id) it is parsed as an Order GID
// and normalized to its canonical decimal resource id; the legacy
// numeric `id` field is accepted only as a pure-decimal alternative and
// must agree with the GID when both are present. Both forms name the
// same Shopify resource (the GID suffix IS the legacy resource id), and
// the same normalization is used for GetOrder, so webhook and read
// paths always share one external order identity.
func WebhookOrderID(raw []byte) (string, bool) {
	var minimal struct {
		ID                json.Number `json:"id"`
		AdminGraphQLAPIID string      `json:"admin_graphql_api_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&minimal); err != nil {
		return "", false
	}
	fromGID := ""
	if strings.TrimSpace(minimal.AdminGraphQLAPIID) != "" {
		parsed, err := ParseGID(minimal.AdminGraphQLAPIID, ResourceOrder)
		if err != nil {
			return "", false
		}
		fromGID = parsed
	}
	fromNumber := ""
	if strings.TrimSpace(minimal.ID.String()) != "" {
		parsed, err := CanonicalDecimalID(minimal.ID.String())
		if err != nil {
			return "", false
		}
		fromNumber = parsed
	}
	switch {
	case fromGID != "" && fromNumber != "":
		if fromGID != fromNumber {
			return "", false
		}
		return fromGID, true
	case fromGID != "":
		return fromGID, true
	case fromNumber != "":
		return fromNumber, true
	default:
		return "", false
	}
}
