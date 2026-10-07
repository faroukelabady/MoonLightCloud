package catalog

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strings"
)

// Semantic fingerprinting (R02 remediation). Equal-revision identity must
// compare semantic catalog state, never raw transport bytes: JSON object
// key order, wire whitespace, and non-semantic collection order must not
// create false conflicts.
//
// Ordering semantics are taken from the authoritative Retail domain
// (storage queries define them):
//   - Category parents: ORDERED (category_edges carries position; the
//     snapshot preserves emitted position order).
//   - Product subcategories: ORDERED (product_subcategories position).
//   - Product tags: SET (product_tags has no position; reads ORDER BY
//     tag_id) — canonicalized by sorting.
//   - Translations: keyed by locale (reads ORDER BY locale) — sorted by
//     locale on both sides.
//   - Prices: keyed by currency (reads ORDER BY currency) — sorted by
//     currency on both sides.
// Descriptions normalize like Retail writes (trim; empty becomes absent)
// so legacy empty-string storage never conflicts with nil.

// NormalizedCategory is the comparison form of a category snapshot.
type NormalizedCategory struct {
	CategoryID string        `json:"category_id"`
	Status     string        `json:"status"`
	Names      []CatalogName `json:"names"`
	ParentIDs  []string      `json:"parent_ids"`
	Revision   int64         `json:"catalog_revision"`
	// OnlineEnabled is semantic state: different policy must never be
	// fingerprint-equivalent (Phase 13 §34).
	OnlineEnabled bool `json:"online_enabled"`
}

// NormalizedTag is the comparison form of a tag snapshot.
type NormalizedTag struct {
	TagID    string        `json:"tag_id"`
	Slug     string        `json:"slug"`
	IsActive bool          `json:"is_active"`
	Names    []CatalogName `json:"names"`
	Revision int64         `json:"catalog_revision"`
}

// NormalizedProduct is the comparison form of a product snapshot.
// There is deliberately NO SKU: Product carries no SKU authority anywhere
// (ADR-0049, 17-R0). v1 still ships a frozen `sku` wire field and v2
// carries none — excluding it here keeps the shared catalog_revision
// stream comparing equal across both versions (and across the retired
// field's removal).
type NormalizedProduct struct {
	ProductID string `json:"product_id"`
	// ProductTypeID is structural identity (§43, never inferred).
	ProductTypeID  string                      `json:"product_type_id,omitempty"`
	Name           string                      `json:"name"`
	Description    *string                     `json:"description,omitempty"`
	Translations   []CatalogProductTranslation `json:"translations"`
	Prices         []CatalogPrice              `json:"prices"`
	TopCategoryID  string                      `json:"top_category_id"`
	SubcategoryIDs []string                    `json:"subcategory_ids"`
	TagIDs         []string                    `json:"tag_ids"`
	WidthCM        *int                        `json:"width_cm,omitempty"`
	HeightCM       *int                        `json:"height_cm,omitempty"`
	IsActive       bool                        `json:"is_active"`
	Revision       int64                       `json:"catalog_revision"`
}

func nonNilNames(names []CatalogName) []CatalogName {
	out := make([]CatalogName, 0, len(names))
	out = append(out, names...)
	sort.Slice(out, func(i, j int) bool { return out[i].Locale < out[j].Locale })
	return out
}

func normDescription(desc *string) *string {
	if desc == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*desc)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// NormalizeCategorySnapshot reduces a validated snapshot to comparison
// form. Parent order is preserved (position semantic).
func NormalizeCategorySnapshot(v CategorySnapshot) NormalizedCategory {
	parents := make([]string, 0, len(v.ParentIDs))
	parents = append(parents, v.ParentIDs...)
	return NormalizedCategory{
		CategoryID: v.CategoryID, Status: v.Status,
		Names: nonNilNames(v.Names), ParentIDs: parents, Revision: v.CatalogRevision,
		OnlineEnabled: v.OnlineEnabled,
	}
}

// NormalizeTagSnapshot reduces a validated snapshot to comparison form.
func NormalizeTagSnapshot(v TagSnapshot) NormalizedTag {
	return NormalizedTag{
		TagID: v.TagID, Slug: v.Slug, IsActive: v.IsActive,
		Names: nonNilNames(v.Names), Revision: v.CatalogRevision,
	}
}

// NormalizeProductSnapshot reduces a validated snapshot to comparison
// form: tags sorted (set), translations by locale, prices by currency,
// ordered relations preserved.
func NormalizeProductSnapshot(v ProductSnapshot) NormalizedProduct {
	tags := make([]string, 0, len(v.TagIDs))
	tags = append(tags, v.TagIDs...)
	sort.Strings(tags)
	subs := make([]string, 0, len(v.SubcategoryIDs))
	subs = append(subs, v.SubcategoryIDs...)
	translations := make([]CatalogProductTranslation, 0, len(v.Translations))
	for _, translation := range v.Translations {
		translations = append(translations, CatalogProductTranslation{
			Locale: translation.Locale, Name: translation.Name,
			Description: normDescription(translation.Description),
		})
	}
	sort.Slice(translations, func(i, j int) bool { return translations[i].Locale < translations[j].Locale })
	prices := make([]CatalogPrice, 0, len(v.Prices))
	prices = append(prices, v.Prices...)
	sort.Slice(prices, func(i, j int) bool { return prices[i].Currency < prices[j].Currency })
	return NormalizedProduct{
		ProductID: v.ProductID, ProductTypeID: v.ProductTypeID, Name: v.Name,
		Description:  normDescription(v.Description),
		Translations: translations, Prices: prices,
		TopCategoryID: v.TopCategoryID, SubcategoryIDs: subs, TagIDs: tags,
		WidthCM: v.WidthCM, HeightCM: v.HeightCM,
		IsActive: v.IsActive, Revision: v.CatalogRevision,
	}
}

// fingerprint marshals the normalized form (struct field order is fixed)
// and hashes it. Envelope metadata (event_id, received_at, whitespace,
// key order) never participates.
func fingerprint(normalized any) [32]byte {
	encoded, err := json.Marshal(normalized)
	if err != nil {
		// Normalized structs contain no unmarshalable values by
		// construction; a failure here is a programmer error. Hash the
		// error deterministically rather than panicking in projection.
		return sha256.Sum256([]byte("fingerprint-encode-error:" + err.Error()))
	}
	return sha256.Sum256(encoded)
}

// FingerprintCategory returns the semantic identity of a category snapshot.
func FingerprintCategory(v CategorySnapshot) [32]byte {
	return fingerprint(NormalizeCategorySnapshot(v))
}

// FingerprintTag returns the semantic identity of a tag snapshot.
func FingerprintTag(v TagSnapshot) [32]byte {
	return fingerprint(NormalizeTagSnapshot(v))
}

// FingerprintProduct returns the semantic identity of a product snapshot.
func FingerprintProduct(v ProductSnapshot) [32]byte {
	return fingerprint(NormalizeProductSnapshot(v))
}

// NormalizedProductSalesPolicy is the comparison form of a policy
// snapshot. There are no collections: the five scalar fields are the
// entire semantic state (envelope excluded by construction).
type NormalizedProductSalesPolicy struct {
	ProductID             string `json:"product_id"`
	SalesPolicyRevision   int64  `json:"sales_policy_revision"`
	SellOffline           bool   `json:"sell_offline"`
	SellOnline            bool   `json:"sell_online"`
	OnlineAllocationLimit *int   `json:"online_allocation_limit,omitempty"`
}

// NormalizeProductSalesPolicySnapshot reduces a validated snapshot to
// comparison form.
func NormalizeProductSalesPolicySnapshot(v ProductSalesPolicySnapshot) NormalizedProductSalesPolicy {
	return NormalizedProductSalesPolicy{
		ProductID: v.ProductID, SalesPolicyRevision: v.SalesPolicyRevision,
		SellOffline: v.SellOffline, SellOnline: v.SellOnline,
		OnlineAllocationLimit: v.OnlineAllocationLimit,
	}
}

// FingerprintProductSalesPolicy returns the semantic identity of a policy
// snapshot.
func FingerprintProductSalesPolicy(v ProductSalesPolicySnapshot) [32]byte {
	return fingerprint(NormalizeProductSalesPolicySnapshot(v))
}

// NormalizedProductInventory is the comparison form of an inventory
// snapshot. The three scalar fields are the entire semantic state
// (envelope excluded by construction).
type NormalizedProductInventory struct {
	ProductID         string `json:"product_id"`
	InventoryRevision int64  `json:"inventory_revision"`
	StockQuantity     int    `json:"stock_quantity"`
}

// NormalizeProductInventorySnapshot reduces a validated snapshot to
// comparison form.
func NormalizeProductInventorySnapshot(v ProductInventorySnapshot) NormalizedProductInventory {
	return NormalizedProductInventory{
		ProductID: v.ProductID, InventoryRevision: v.InventoryRevision,
		StockQuantity: v.StockQuantity,
	}
}

// FingerprintProductInventory returns the semantic identity of an
// inventory snapshot.
func FingerprintProductInventory(v ProductInventorySnapshot) [32]byte {
	return fingerprint(NormalizeProductInventorySnapshot(v))
}

// FingerprintProductConfigurations hashes the normalized configuration
// state (Phase 15 §48): deterministic over configuration IDs, codes,
// labels as published, price deltas, enabled, position and per-row
// revisions — canonical ordering, no timestamps or row order. It is the
// stored source_payload_hash for configuration events and participates
// in provider operation identity through DesiredProduct.
func FingerprintProductConfigurations(snapshot ProductConfigurationsSnapshot) [32]byte {
	return fingerprint(NormalizeProductConfigurations(snapshot))
}
