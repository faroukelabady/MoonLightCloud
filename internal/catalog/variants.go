package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Phase 17 product & physical variant architecture. SKU and inventory
// ownership live on ProductVariant (Retail authority); Cloud projects two
// independent variant streams:
//
//   - catalog.product_variant.snapshot.v1 — variant identity, prices,
//     option attributes (display labels), position and the normalized
//     combination_key, gated by variant_revision (inside the product's
//     catalog_revision context).
//   - inventory.product_variant.snapshot.v1 — variant-scoped stock gated
//     by inventory_revision, mirroring inventory.product.snapshot.v1
//     semantics plus the policy/catalog context Retail sends alongside.
//
// Mirror fields carried by both payloads (stock on the catalog stream,
// sku/policy/catalog on the inventory stream) are shape-validated but
// never semantic: each stream owns exactly one revision gate, so a
// mirror advancing on the other stream can never fabricate an
// equal-revision conflict.

// Registered variant event types (wired in internal/app).
const (
	EventProductVariantSnapshotV1          = "catalog.product_variant.snapshot.v1"
	EventInventoryProductVariantSnapshotV1 = "inventory.product_variant.snapshot.v1"
)

// Variant attribute bounds (bounded strings, desktop-enforced domain).
const (
	MaxVariantAttributes   = 32
	MaxVariantCodeRunes    = 64
	MaxVariantLabelRunes   = 200
	MaxCombinationKeyRunes = 512
)

// VariantAttribute is one physical option of a variant with bilingual
// display labels. Labels are projection-only; identity is
// (definition_code, value_code).
type VariantAttribute struct {
	DefinitionCode   string  `json:"definition_code"`
	ValueCode        string  `json:"value_code"`
	NameAR           string  `json:"name_ar"`
	NameEN           *string `json:"name_en,omitempty"`
	DefinitionNameAR string  `json:"definition_name_ar"`
	DefinitionNameEN *string `json:"definition_name_en,omitempty"`
	Position         int     `json:"position"`
}

// ProductVariantSnapshot is the authoritative variant state at a
// variant_revision. Money is int64 minor units (never floats); nullable
// USD preserves blank-vs-zero exactly like admin payloads. StockQuantity/
// InventoryRevision are non-semantic mirrors of the inventory stream
// (validated for shape only, never projected here).
type ProductVariantSnapshot struct {
	VariantID         string             `json:"variant_id"`
	ProductID         string             `json:"product_id"`
	SKU               string             `json:"sku"`
	IsActive          bool               `json:"is_active"`
	Deleted           bool               `json:"deleted"`
	PriceEGPCents     *int64             `json:"price_egp_cents"`
	PriceUSDCents     *int64             `json:"price_usd_cents"`
	StockQuantity     int                `json:"stock_quantity"`
	InventoryRevision int64              `json:"inventory_revision"`
	VariantRevision   int64              `json:"variant_revision"`
	Position          int                `json:"position"`
	CombinationKey    string             `json:"combination_key"`
	Attributes        []VariantAttribute `json:"attributes"`
	CatalogRevision   int64              `json:"catalog_revision"`
}

// DecodeProductVariantSnapshot parses canonical payload bytes. Decoding
// into int64 rejects float JSON money and overflow at parse time.
func DecodeProductVariantSnapshot(raw json.RawMessage) (ProductVariantSnapshot, error) {
	var p ProductVariantSnapshot
	if err := decodePayload(EventProductVariantSnapshotV1, raw, &p); err != nil {
		return ProductVariantSnapshot{}, err
	}
	return p, nil
}

// ValidateProductVariantSnapshot enforces the Phase 17 variant contract:
// UUID identities, bounded SKU without control whitespace, non-negative
// int64 minor-unit money, bounded normalized combination key, bounded
// attribute list with unique definition codes and bilingual labels, and
// positive stream revisions. Deleted is an optional tombstone marker
// (default false): tombstones are stored as rows, never deletes. The
// inventory mirror fields are shape-checked only (see package comment).
func ValidateProductVariantSnapshot(p ProductVariantSnapshot) (ProductVariantSnapshot, error) {
	fail := func(format string, args ...any) (ProductVariantSnapshot, error) {
		return ProductVariantSnapshot{}, apperr.New(apperr.Unprocessable, "invalid "+EventProductVariantSnapshotV1+": "+fmt.Sprintf(format, args...))
	}
	if !isUUID(p.VariantID) {
		return fail("variant_id must be a UUID")
	}
	if !isUUID(p.ProductID) {
		return fail("product_id must be a UUID")
	}
	sku := strings.TrimSpace(p.SKU)
	if sku == "" || len(sku) > 64 || strings.ContainsAny(sku, "\r\n\t") {
		return fail("sku must be 1..64 chars without control whitespace")
	}
	if utf8Len(p.CombinationKey) > MaxCombinationKeyRunes || strings.ContainsAny(p.CombinationKey, "\r\n\t") {
		return fail("combination_key must be %d runes at most without control whitespace", MaxCombinationKeyRunes)
	}
	if p.PriceEGPCents != nil && *p.PriceEGPCents < 0 {
		return fail("price_egp_cents must not be negative")
	}
	if p.PriceUSDCents != nil && *p.PriceUSDCents < 0 {
		return fail("price_usd_cents must not be negative")
	}
	if p.StockQuantity < 0 || p.StockQuantity > math.MaxInt32 {
		return fail("stock_quantity must be 0..MaxInt32")
	}
	if p.InventoryRevision < 0 {
		return fail("inventory_revision must not be negative")
	}
	if p.Position < 0 {
		return fail("position must not be negative")
	}
	if !validRevision(p.VariantRevision) {
		return fail("variant_revision must be >= 1")
	}
	if !validRevision(p.CatalogRevision) {
		return fail("catalog_revision must be >= 1")
	}
	if len(p.Attributes) > MaxVariantAttributes {
		return fail("at most %d attributes", MaxVariantAttributes)
	}
	seen := make(map[string]bool, len(p.Attributes))
	for _, attr := range p.Attributes {
		if !validCode(attr.DefinitionCode) {
			return fail("definition_code must be 1..64 chars without control whitespace")
		}
		if seen[attr.DefinitionCode] {
			return fail("duplicate definition_code")
		}
		seen[attr.DefinitionCode] = true
		if !validCode(attr.ValueCode) {
			return fail("value_code must be 1..64 chars without control whitespace")
		}
		if !validVariantLabel(attr.NameAR) {
			return fail("name_ar must be 1..200 runes")
		}
		if !validVariantLabel(attr.DefinitionNameAR) {
			return fail("definition_name_ar must be 1..200 runes")
		}
		if !validOptionalVariantLabel(attr.NameEN) {
			return fail("name_en must be at most 200 runes")
		}
		if !validOptionalVariantLabel(attr.DefinitionNameEN) {
			return fail("definition_name_en must be at most 200 runes")
		}
		if attr.Position < 0 {
			return fail("attribute position must not be negative")
		}
	}
	if p.Attributes == nil {
		p.Attributes = []VariantAttribute{}
	}
	return p, nil
}

// validCode bounds identity codes (definition_code/value_code).
func validCode(code string) bool {
	if code == "" || len(code) > MaxVariantCodeRunes {
		return false
	}
	return !strings.ContainsAny(code, "\r\n\t")
}

// validVariantLabel bounds required display labels.
func validVariantLabel(name string) bool {
	return validName(name)
}

// validOptionalVariantLabel bounds optional display labels: absent or
// bounded, blank allowed (mirrors nullable name_en storage).
func validOptionalVariantLabel(name *string) bool {
	if name == nil {
		return true
	}
	return utf8Len(*name) <= MaxVariantLabelRunes
}

func utf8Len(s string) int { return len([]rune(s)) }

// ProductVariantInventorySnapshot is the authoritative variant-scoped
// inventory state at an inventory_revision (Phase 17): variant identity,
// monotonic inventory_revision, resulting whole-unit stock_quantity, and
// the policy/catalog context Retail mirrors alongside (non-semantic for
// this stream's gate).
type ProductVariantInventorySnapshot struct {
	VariantID             string `json:"variant_id"`
	ProductID             string `json:"product_id"`
	SKU                   string `json:"sku"`
	StockQuantity         int    `json:"stock_quantity"`
	InventoryRevision     int64  `json:"inventory_revision"`
	Ready                 bool   `json:"ready"`
	SellOnline            bool   `json:"sell_online"`
	OnlineAllocationLimit *int   `json:"online_allocation_limit"`
	PolicyRevision        int64  `json:"policy_revision"`
	CatalogRevision       int64  `json:"catalog_revision"`
}

// DecodeProductVariantInventorySnapshot parses canonical payload bytes.
func DecodeProductVariantInventorySnapshot(raw json.RawMessage) (ProductVariantInventorySnapshot, error) {
	var p ProductVariantInventorySnapshot
	if err := decodePayload(EventInventoryProductVariantSnapshotV1, raw, &p); err != nil {
		return ProductVariantInventorySnapshot{}, err
	}
	return p, nil
}

// ValidateProductVariantInventorySnapshot enforces the desktop variant
// inventory contract: UUID identities, bounded SKU, positive stream
// revision, whole-unit quantity in 0..MaxInt32 (the exact Retail ledger
// domain), non-negative mirror revisions, and a nullable 0..MaxInt32
// allocation cap.
func ValidateProductVariantInventorySnapshot(p ProductVariantInventorySnapshot) (ProductVariantInventorySnapshot, error) {
	fail := func(format string, args ...any) (ProductVariantInventorySnapshot, error) {
		return ProductVariantInventorySnapshot{}, apperr.New(apperr.Unprocessable, "invalid "+EventInventoryProductVariantSnapshotV1+": "+fmt.Sprintf(format, args...))
	}
	if !isUUID(p.VariantID) {
		return fail("variant_id must be a UUID")
	}
	if !isUUID(p.ProductID) {
		return fail("product_id must be a UUID")
	}
	sku := strings.TrimSpace(p.SKU)
	if sku == "" || len(sku) > 64 || strings.ContainsAny(sku, "\r\n\t") {
		return fail("sku must be 1..64 chars without control whitespace")
	}
	if p.StockQuantity < 0 || p.StockQuantity > math.MaxInt32 {
		return fail("stock_quantity must be 0..MaxInt32")
	}
	if !validRevision(p.InventoryRevision) {
		return fail("inventory_revision must be >= 1")
	}
	if p.PolicyRevision < 0 {
		return fail("policy_revision must not be negative")
	}
	if !validRevision(p.CatalogRevision) {
		return fail("catalog_revision must be >= 1")
	}
	if p.OnlineAllocationLimit != nil {
		if *p.OnlineAllocationLimit < 0 || *p.OnlineAllocationLimit > math.MaxInt32 {
			return fail("online_allocation_limit must be 0..MaxInt32")
		}
	}
	return p, nil
}

// NormalizedProductVariant is the comparison form of a variant snapshot.
// Only semantic state participates: stock/catalog mirrors carried by the
// payload are excluded so independent streams can never fabricate an
// equal-revision conflict. Attributes compare as an ordered list
// (position carries order; sorted deterministically).
type NormalizedProductVariant struct {
	VariantID      string             `json:"variant_id"`
	ProductID      string             `json:"product_id"`
	SKU            string             `json:"sku"`
	IsActive       bool               `json:"is_active"`
	Deleted        bool               `json:"deleted"`
	PriceEGPCents  *int64             `json:"price_egp_cents"`
	PriceUSDCents  *int64             `json:"price_usd_cents"`
	Position       int                `json:"position"`
	CombinationKey string             `json:"combination_key"`
	Attributes     []VariantAttribute `json:"attributes"`
	Revision       int64              `json:"variant_revision"`
}

// NormalizeProductVariantSnapshot reduces a validated variant snapshot to
// comparison form: attributes sorted by (position, definition_code), the
// exact read order, so wire order noise is never semantic.
func NormalizeProductVariantSnapshot(v ProductVariantSnapshot) NormalizedProductVariant {
	attrs := make([]VariantAttribute, 0, len(v.Attributes))
	attrs = append(attrs, v.Attributes...)
	sort.Slice(attrs, func(i, j int) bool {
		if attrs[i].Position != attrs[j].Position {
			return attrs[i].Position < attrs[j].Position
		}
		return attrs[i].DefinitionCode < attrs[j].DefinitionCode
	})
	return NormalizedProductVariant{
		VariantID: v.VariantID, ProductID: v.ProductID, SKU: v.SKU,
		IsActive: v.IsActive, Deleted: v.Deleted,
		PriceEGPCents: v.PriceEGPCents, PriceUSDCents: v.PriceUSDCents,
		Position: v.Position, CombinationKey: v.CombinationKey,
		Attributes: attrs, Revision: v.VariantRevision,
	}
}

// FingerprintProductVariant returns the semantic identity of a variant
// snapshot (stored source_payload_hash for variant events).
func FingerprintProductVariant(v ProductVariantSnapshot) [32]byte {
	return fingerprint(NormalizeProductVariantSnapshot(v))
}

// NormalizedProductVariantInventory is the comparison form of a variant
// inventory snapshot. Mirror fields (sku, ready, sell_online, allocation,
// policy/catalog/variant revisions) are non-semantic: only the stream's
// own revision and stock decide identity.
type NormalizedProductVariantInventory struct {
	VariantID         string `json:"variant_id"`
	ProductID         string `json:"product_id"`
	InventoryRevision int64  `json:"inventory_revision"`
	StockQuantity     int    `json:"stock_quantity"`
}

// NormalizeProductVariantInventorySnapshot reduces a validated snapshot
// to comparison form.
func NormalizeProductVariantInventorySnapshot(v ProductVariantInventorySnapshot) NormalizedProductVariantInventory {
	return NormalizedProductVariantInventory{
		VariantID: v.VariantID, ProductID: v.ProductID,
		InventoryRevision: v.InventoryRevision, StockQuantity: v.StockQuantity,
	}
}

// FingerprintProductVariantInventory returns the semantic identity of a
// variant inventory snapshot.
func FingerprintProductVariantInventory(v ProductVariantInventorySnapshot) [32]byte {
	return fingerprint(NormalizeProductVariantInventorySnapshot(v))
}
