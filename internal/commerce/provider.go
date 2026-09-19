// Package commerce reserves the future CommerceProvider adapter boundary.
// Phase 1A implements nothing here. See ADR-0007 and
// docs/architecture/overview.md.
package commerce

import "context"

// Order is a placeholder sketch of the direction, not a contract.
type Order struct {
	ExternalID string
}

// Provider is the future seam for Shopify / WooCommerce / others.
// Catalog sync, orders, and webhooks arrive in later phases behind this
// interface so the core never imports a vendor SDK.
type Provider interface {
	Name() string
	// Future: ListProducts, GetOrder, webhooks...
	Close(ctx context.Context) error
}
