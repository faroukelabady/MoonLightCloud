package http

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// Order list continuation cursors are opaque, versioned, bounded tokens.
// They carry only stable list-position metadata (timestamp, provider,
// order identity) plus the filter set they were issued for — never
// customer PII, money, or secrets. A cursor reused under different
// filters is rejected so pages cannot silently skip data.
const (
	orderCursorVersion = 1
	// orderCursorMaxLength bounds decode input: tokens are ~150 bytes.
	orderCursorMaxLength = 512
)

type orderCursorToken struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"t"`
	Provider  string `json:"p"`
	Order     string `json:"o"`
	FilterP   string `json:"fp"`
	FilterS   string `json:"fs"`
}

// encodeOrderCursor renders one continuation token for the last returned
// row under the current filters.
func encodeOrderCursor(cursor *orders.OrderCursor, provider, status string) (string, error) {
	token := orderCursorToken{
		Version:   orderCursorVersion,
		CreatedAt: cursor.CreatedAt.UTC().Format(time.RFC3339Nano),
		Provider:  cursor.ProviderKey, Order: cursor.ExternalOrderID,
		FilterP: provider, FilterS: status,
	}
	raw, err := json.Marshal(token)
	if err != nil {
		return "", apperr.Wrap(apperr.Internal, "order cursor", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeOrderCursor validates one client-supplied token and binds it to
// the current request filters. Every rejection is a 400: no server
// error, no panic, no secret or PII in the error.
func decodeOrderCursor(value, provider, status string) (*orders.OrderCursor, error) {
	invalid := func() (*orders.OrderCursor, error) {
		return nil, apperr.New(apperr.InvalidInput, "invalid cursor")
	}
	if len(value) == 0 || len(value) > orderCursorMaxLength {
		return invalid()
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var token orderCursorToken
	if err := decoder.Decode(&token); err != nil {
		return invalid()
	}
	if token.Version != orderCursorVersion {
		return invalid()
	}
	if token.FilterP != provider || token.FilterS != status {
		return invalid()
	}
	created, err := time.Parse(time.RFC3339Nano, token.CreatedAt)
	if err != nil || created.IsZero() {
		return invalid()
	}
	if !printableBounded(token.Provider, 64) || !printableBounded(token.Order, 32) ||
		!printableBounded(token.FilterP, 64) || !printableBounded(token.FilterS, 16) {
		return invalid()
	}
	if token.Provider == "" || token.Order == "" {
		return invalid()
	}
	// Canonical round-trip: reject trailing garbage or reordered
	// encodings that decode but were never issued here.
	canonical, err := encodeOrderCursor(&orders.OrderCursor{
		CreatedAt: created, ProviderKey: token.Provider, ExternalOrderID: token.Order,
	}, token.FilterP, token.FilterS)
	if err != nil || canonical != value {
		return invalid()
	}
	return &orders.OrderCursor{
		CreatedAt: created, ProviderKey: token.Provider, ExternalOrderID: token.Order,
	}, nil
}

// printableBounded accepts short printable ASCII without control
// characters; cursors travel in URL queries and must stay inert.
func printableBounded(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 32 || value[i] == 127 {
			return false
		}
	}
	return true
}
