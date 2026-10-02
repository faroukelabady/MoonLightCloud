package shopify

import (
	"fmt"
	"regexp"
	"strings"
)

// Shopify resource identity normalization (ADR-0044).
//
// Shopify GraphQL resources are addressed as GIDs of the form
// gid://shopify/<Type>/<decimal>, where the decimal suffix is the same
// number the REST-era "legacy" API and every webhook payload expose as
// the resource's numeric id (GraphQL legacyResourceId). The frozen
// generic commerce tables bound external identities to 32 characters
// (commerce_online_orders.external_order_id,
// commerce_online_order_lines.external_product_id) and the frozen order
// domain canonicalizes external order identity as positive decimal
// (orders.CanonicalExternalOrderID), so the durable external identity is
// the canonical decimal resource id. GIDs are parsed and type-checked at
// the API boundary and reconstructed deterministically before use:
//
//	gid://shopify/Product/8294713837621  <->  8294713837621
//
// Type validation prevents resource-type confusion: a ProductVariant GID
// is never accepted where a Product GID is required.
const (
	ResourceOrder          = "Order"
	ResourceProduct        = "Product"
	ResourceProductVariant = "ProductVariant"
	ResourceInventoryItem  = "InventoryItem"
	ResourceLineItem       = "LineItem"
	ResourceLocation       = "Location"
	ResourcePublication    = "Publication"
)

var gidPattern = regexp.MustCompile(`^gid://shopify/([A-Za-z]+)/([0-9]+)$`)

// ParseGID validates a canonical GID of the expected resource type and
// returns its canonical decimal resource id. Wrong type, malformed
// input, empty suffix, non-canonical numerics, and surrounding
// whitespace all fail closed.
func ParseGID(raw, wantType string) (string, error) {
	matches := gidPattern.FindStringSubmatch(raw)
	if matches == nil {
		return "", fmt.Errorf("malformed shopify gid")
	}
	resourceType, decimal := matches[1], matches[2]
	if resourceType != wantType {
		return "", fmt.Errorf("shopify gid type mismatch: want %s", wantType)
	}
	canonical, err := CanonicalDecimalID(decimal)
	if err != nil {
		return "", err
	}
	return canonical, nil
}

// CanonicalDecimalID normalizes a decimal resource id: digits only, no
// leading zeros, no surrounding whitespace, bounded to 19 digits (int64
// range), value > 0.
func CanonicalDecimalID(raw string) (string, error) {
	trimmed := raw
	if trimmed == "" || len(trimmed) > 19 {
		return "", fmt.Errorf("invalid shopify resource id")
	}
	if strings.HasPrefix(trimmed, "0") && len(trimmed) > 1 {
		return "", fmt.Errorf("invalid shopify resource id")
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("invalid shopify resource id")
		}
	}
	if trimmed == "0" {
		return "", fmt.Errorf("invalid shopify resource id")
	}
	return trimmed, nil
}

// FormatGID reconstructs the canonical GID for a resource type and
// canonical decimal id. Types are internal constants only: no
// caller-supplied type strings reach the wire.
func FormatGID(resourceType, decimalID string) string {
	return "gid://shopify/" + resourceType + "/" + decimalID
}

// CanonicalExternalID is the durable mapping/order identity form for one
// Shopify resource: canonical decimal. See the ADR-0044 normalization
// note above.
func CanonicalExternalID(gid, wantType string) (string, error) {
	return ParseGID(gid, wantType)
}
