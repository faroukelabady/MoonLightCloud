package catalog

import "context"

// Phase 13 §52/§53: the provider-neutral ONLINE eligibility result.
// Reasons are stable internal codes — never provider-specific:
//
//	ALLOWED                     — every relevant Category node permits ONLINE
//	CATEGORY_ONLINE_DISABLED    — intentional policy suppression (a
//	                              relevant node — the Product's top
//	                              category, a subcategory, or any DAG
//	                              ancestor of either — is disabled)
//	CATEGORY_STATE_INCOMPLETE   — required Category hierarchy state is
//	                              missing/unresolvable (fail safe)
const (
	OnlineAllowed                = "ALLOWED"
	OnlineBlockedCategory        = "CATEGORY_ONLINE_DISABLED"
	OnlineBlockedStateIncomplete = "CATEGORY_STATE_INCOMPLETE"
)

// ProductOnlinePolicy is the canonical effective-online result for one
// Product's Category hierarchy. One calculation (the
// catalog_product_online_state view) feeds commerce publication and
// Catalog Health; no adapter ever traverses the DAG.
type ProductOnlinePolicy struct {
	ProductID          string
	StoreID            *string
	Allowed            bool
	Reason             string
	BlockingCategoryID *string
	// PolicyFingerprint is the ELIGIBILITY identity of the relevant
	// Category policy context: (category_id, depth, online_enabled) per
	// relevant node, canonical order. Pure policy state — renames and
	// translations never change it (§100).
	PolicyFingerprint string
	// PolicyVersion is the revision digest of the same nodes: the
	// POLICY GENERATION marker that advances across every category
	// revision (including an enabled->disabled->enabled cycle), keeping
	// provider operation identity free of stale idempotency-key reuse
	// (§96). It may rotate on renames because commerce payloads carry
	// Category display metadata (§100 escape clause).
	PolicyVersion string
}

// OnlinePolicyReader reads the canonical policy state.
type OnlinePolicyReader interface {
	CatalogProductOnlinePolicy(ctx context.Context, productID string) (ProductOnlinePolicy, error)
}

// GetProductOnlinePolicy returns the canonical Category-hierarchy ONLINE
// policy for one Product (Phase 13).
func (s Service) GetProductOnlinePolicy(ctx context.Context, id string) (ProductOnlinePolicy, error) {
	return s.repo.CatalogProductOnlinePolicy(ctx, id)
}
