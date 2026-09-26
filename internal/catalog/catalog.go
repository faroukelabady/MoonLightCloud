// Package catalog owns the catalog.*.snapshot.v1 Cloud DTOs, their strict
// ingestion-time validators, the three current-state projectors, and the
// read service future phases consume. Field names mirror the MoonLightRetail
// Phase-5A snapshot DTOs exactly (read-only contract, never a shared
// module). Ingestion validates wire/schema invariants only; dependency
// resolution, DAG defense, and revision ordering belong to projection.
package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Registered event types (wired in internal/app).
const (
	EventCategorySnapshotV1 = "catalog.category.snapshot.v1"
	EventTagSnapshotV1      = "catalog.tag.snapshot.v1"
	EventProductSnapshotV1  = "catalog.product.snapshot.v1"
)

// Valid category lifecycle states (desktop-enforced).
const (
	CategoryActive   = "active"
	CategoryHidden   = "hidden"
	CategoryArchived = "archived"
)

// Supported price currencies and translation locales.
const (
	CurrencyEGP = "EGP"
	CurrencyUSD = "USD"
	LocaleAR    = "ar"
	LocaleEN    = "en"
)

// CatalogName is one locale/name pair.
type CatalogName struct {
	Locale string `json:"locale"`
	Name   string `json:"name"`
}

// CatalogProductTranslation is one product locale row.
type CatalogProductTranslation struct {
	Locale      string  `json:"locale"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// CatalogPrice is one currency price row with optional cost.
type CatalogPrice struct {
	Currency   string `json:"currency"`
	PriceCents int64  `json:"price_cents"`
	CostCents  *int64 `json:"cost_cents,omitempty"`
}

// CategorySnapshot is the authoritative category state at a revision.
type CategorySnapshot struct {
	CategoryID      string        `json:"category_id"`
	Status          string        `json:"status"`
	Names           []CatalogName `json:"names"`
	ParentIDs       []string      `json:"parent_ids"`
	CatalogRevision int64         `json:"catalog_revision"`
}

// TagSnapshot is the authoritative tag state at a revision.
type TagSnapshot struct {
	TagID           string        `json:"tag_id"`
	Slug            string        `json:"slug"`
	IsActive        bool          `json:"is_active"`
	Names           []CatalogName `json:"names"`
	CatalogRevision int64         `json:"catalog_revision"`
}

// ProductSnapshot is the authoritative product state at a revision.
type ProductSnapshot struct {
	ProductID       string                      `json:"product_id"`
	SKU             string                      `json:"sku"`
	Name            string                      `json:"name"`
	Description     *string                     `json:"description,omitempty"`
	Translations    []CatalogProductTranslation `json:"translations"`
	Prices          []CatalogPrice              `json:"prices"`
	TopCategoryID   string                      `json:"top_category_id"`
	SubcategoryIDs  []string                    `json:"subcategory_ids"`
	TagIDs          []string                    `json:"tag_ids"`
	WidthCM         *int                        `json:"width_cm,omitempty"`
	HeightCM        *int                        `json:"height_cm,omitempty"`
	IsActive        bool                        `json:"is_active"`
	CatalogRevision int64                       `json:"catalog_revision"`
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}

func validName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && utf8.RuneCountInString(name) <= 200
}

func validLocale(locale string) bool { return locale == LocaleAR || locale == LocaleEN }

func validRevision(revision int64) bool { return revision >= 1 }

func validSlug(slug string) bool {
	if slug == "" {
		return false
	}
	for _, r := range slug {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func validDimension(dim *int) bool {
	return dim == nil || (*dim >= 1 && *dim <= math.MaxInt32)
}

func decodePayload(eventType string, raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(out); err != nil {
		return apperr.New(apperr.Unprocessable, "invalid "+eventType+": not a valid object")
	}
	if dec.More() {
		return apperr.New(apperr.Unprocessable, "invalid "+eventType+": trailing data")
	}
	return nil
}

// DecodeCategorySnapshot parses the canonical payload bytes.
func DecodeCategorySnapshot(raw json.RawMessage) (CategorySnapshot, error) {
	var p CategorySnapshot
	if err := decodePayload(EventCategorySnapshotV1, raw, &p); err != nil {
		return CategorySnapshot{}, err
	}
	return p, nil
}

// ValidateCategorySnapshot enforces every invariant the desktop category
// path guarantees: UUID identity, lifecycle enum, at least one valid
// translation, acyclic-safe parent-ID shape (no self edge, no duplicates),
// and a positive revision. Graph semantics (existence, cycles, depth) are
// projection-time checks against current state, never ingestion blocks.
func ValidateCategorySnapshot(p CategorySnapshot) (CategorySnapshot, error) {
	fail := func(format string, args ...any) (CategorySnapshot, error) {
		return CategorySnapshot{}, apperr.New(apperr.Unprocessable, "invalid "+EventCategorySnapshotV1+": "+fmt.Sprintf(format, args...))
	}
	if !isUUID(p.CategoryID) {
		return fail("category_id must be a UUID")
	}
	if p.Status != CategoryActive && p.Status != CategoryHidden && p.Status != CategoryArchived {
		return fail("status must be active|hidden|archived")
	}
	if len(p.Names) == 0 {
		return fail("at least one translation is required")
	}
	for _, name := range p.Names {
		if !validLocale(name.Locale) {
			return fail("locale must be ar|en")
		}
		if !validName(name.Name) {
			return fail("name must be 1..200 runes")
		}
	}
	seen := make(map[string]bool, len(p.ParentIDs))
	for _, parent := range p.ParentIDs {
		if !isUUID(parent) {
			return fail("parent_id must be a UUID")
		}
		if parent == p.CategoryID {
			return fail("category must not parent itself")
		}
		if seen[parent] {
			return fail("duplicate parent_id")
		}
		seen[parent] = true
	}
	if p.ParentIDs == nil {
		p.ParentIDs = []string{}
	}
	if !validRevision(p.CatalogRevision) {
		return fail("catalog_revision must be >= 1")
	}
	return p, nil
}

// DecodeTagSnapshot parses the canonical payload bytes.
func DecodeTagSnapshot(raw json.RawMessage) (TagSnapshot, error) {
	var p TagSnapshot
	if err := decodePayload(EventTagSnapshotV1, raw, &p); err != nil {
		return TagSnapshot{}, err
	}
	return p, nil
}

// ValidateTagSnapshot enforces tag identity (UUID, lowercase slug shape),
// translation bounds, and a positive revision.
func ValidateTagSnapshot(p TagSnapshot) (TagSnapshot, error) {
	fail := func(format string, args ...any) (TagSnapshot, error) {
		return TagSnapshot{}, apperr.New(apperr.Unprocessable, "invalid "+EventTagSnapshotV1+": "+fmt.Sprintf(format, args...))
	}
	if !isUUID(p.TagID) {
		return fail("tag_id must be a UUID")
	}
	if !validSlug(p.Slug) {
		return fail("slug must match ^[a-z0-9-]+$")
	}
	for _, name := range p.Names {
		if !validLocale(name.Locale) {
			return fail("locale must be ar|en")
		}
		if !validName(name.Name) {
			return fail("name must be 1..200 runes")
		}
	}
	if p.Names == nil {
		p.Names = []CatalogName{}
	}
	if !validRevision(p.CatalogRevision) {
		return fail("catalog_revision must be >= 1")
	}
	return p, nil
}

// DecodeProductSnapshot parses the canonical payload bytes. Decoding into
// int64 rejects float JSON money and overflow at parse time.
func DecodeProductSnapshot(raw json.RawMessage) (ProductSnapshot, error) {
	var p ProductSnapshot
	if err := decodePayload(EventProductSnapshotV1, raw, &p); err != nil {
		return ProductSnapshot{}, err
	}
	return p, nil
}

// ValidateProductSnapshot enforces every invariant the desktop product path
// guarantees: UUID identity, SKU bounds, bilingual-capable names, nonneg
// integer money per EGP|USD row, positive whole-centimeter dimensions when
// present, UUID relations without duplicates, and a positive revision.
// Legacy NULL dimensions and empty prices synchronize as unknown rather
// than failing: storage permits them, so ingestion must too. Reachability
// and root rules are projection-time checks, never ingestion blocks.
func ValidateProductSnapshot(p ProductSnapshot) (ProductSnapshot, error) {
	fail := func(format string, args ...any) (ProductSnapshot, error) {
		return ProductSnapshot{}, apperr.New(apperr.Unprocessable, "invalid "+EventProductSnapshotV1+": "+fmt.Sprintf(format, args...))
	}
	if !isUUID(p.ProductID) {
		return fail("product_id must be a UUID")
	}
	sku := strings.TrimSpace(p.SKU)
	if sku == "" || len(sku) > 64 || strings.ContainsAny(sku, "\r\n\t") {
		return fail("sku must be 1..64 chars without control whitespace")
	}
	if !validName(p.Name) {
		return fail("name must be 1..200 runes")
	}
	for _, price := range p.Prices {
		if price.Currency != CurrencyEGP && price.Currency != CurrencyUSD {
			return fail("price currency must be EGP|USD")
		}
		if price.PriceCents < 0 {
			return fail("price must not be negative")
		}
		if price.CostCents != nil && *price.CostCents < 0 {
			return fail("cost must not be negative")
		}
	}
	if p.Prices == nil {
		p.Prices = []CatalogPrice{}
	}
	for _, translation := range p.Translations {
		if !validLocale(translation.Locale) {
			return fail("locale must be ar|en")
		}
		if !validName(translation.Name) {
			return fail("translation name must be 1..200 runes")
		}
	}
	if p.Translations == nil {
		p.Translations = []CatalogProductTranslation{}
	}
	if !isUUID(p.TopCategoryID) {
		return fail("top_category_id must be a UUID")
	}
	seenSubs := make(map[string]bool, len(p.SubcategoryIDs))
	for _, id := range p.SubcategoryIDs {
		if !isUUID(id) {
			return fail("subcategory_id must be a UUID")
		}
		if seenSubs[id] {
			return fail("duplicate subcategory_id")
		}
		seenSubs[id] = true
	}
	seenTags := make(map[string]bool, len(p.TagIDs))
	for _, id := range p.TagIDs {
		if !isUUID(id) {
			return fail("tag_id must be a UUID")
		}
		if seenTags[id] {
			return fail("duplicate tag_id")
		}
		seenTags[id] = true
	}
	if p.SubcategoryIDs == nil {
		p.SubcategoryIDs = []string{}
	}
	if p.TagIDs == nil {
		p.TagIDs = []string{}
	}
	if !validDimension(p.WidthCM) || !validDimension(p.HeightCM) {
		return fail("dimensions must be positive whole centimeters when present")
	}
	if !validRevision(p.CatalogRevision) {
		return fail("catalog_revision must be >= 1")
	}
	return p, nil
}
