package catalog

import (
	"context"
	"time"
)

// ProductInventory is the projected last-known synchronized Retail stock
// for one product. It is inventory truth, never availability: eligibility
// and caps apply at the availability layer below.
type ProductInventory struct {
	ProductID        string
	StockQuantity    int
	Revision         int64
	SourceEventID    string
	SourceReceivedAt time.Time
	ProjectedAt      time.Time
}

// ProductAvailability is the provider-neutral ONLINE availability derived
// from current product lifecycle, sales policy, and last-known inventory.
// It is a deterministic read: computing it never reserves, decrements,
// writes movements, or consumes allocation.
//
// Ready reports whether all three inputs were present. Missing inputs
// yield safe-zero availability with Ready=false and the corresponding
// missing flag; no time-based staleness is invented (Phase 7C owns
// device connectivity).
type ProductAvailability struct {
	ProductID             string
	ProductActive         bool
	SellOnline            bool
	OnlineAllocationLimit *int
	StockQuantity         *int
	OnlineAvailable       int
	Ready                 bool
	MissingPolicy         bool
	MissingInventory      bool
	InventoryRevision     int64
	InventoryProjectedAt  *time.Time
	InventoryEventID      string
}

// ComputeProductAvailability derives ONLINE availability from current
// inputs. Formula (ADR-0030):
//
//	missing product/policy/inventory → 0, not ready
//	inactive product or !sell_online → 0
//	else max(stock,0), capped by the allocation limit when set
//	(NULL limit = uncapped).
//
// The max() clamp is harmless under the mirrored Retail >= 0 constraint
// and keeps exposure safe if the constraint ever widens.
func ComputeProductAvailability(
	productActive bool, productFound bool,
	sellOnline bool, policyFound bool, allocationLimit *int,
	stockQuantity int, inventoryFound bool,
) (onlineAvailable int, ready bool) {
	if !productFound || !policyFound || !inventoryFound {
		return 0, false
	}
	if !productActive || !sellOnline {
		return 0, true
	}
	sellable := stockQuantity
	if sellable < 0 {
		sellable = 0
	}
	if allocationLimit != nil && sellable > *allocationLimit {
		sellable = *allocationLimit
	}
	return sellable, true
}

// GetProductInventory returns the projected last-known inventory.
func (s Service) GetProductInventory(ctx context.Context, id string) (ProductInventory, error) {
	return s.repo.CatalogProductInventory(ctx, id)
}

// GetProductAvailability returns the derived provider-neutral ONLINE
// availability with source metadata, read consistently in one transaction
// by the adapter.
func (s Service) GetProductAvailability(ctx context.Context, id string) (ProductAvailability, error) {
	return s.repo.CatalogProductAvailability(ctx, id)
}
