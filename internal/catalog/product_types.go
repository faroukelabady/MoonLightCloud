package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Phase 17-R2 ProductType projection (ADR-0050). Retail-authoritative
// structural types: identity, code, translations, status, position,
// revision, allowed dimensions and capabilities. Cloud projects (never
// authority). Type revision gates writes; lifecycle deactivation is a
// state transition, never a delete.

// Registered product-type event type (wired in internal/app).
const EventProductTypeSnapshotV1 = "catalog.product_type.snapshot.v1"

// KnownCapabilityCodes lists capability codes with implemented behavior.
// Unknown codes never alter business behavior (rejected at validation).
var KnownCapabilityCodes = map[string]struct{}{
	"frame_configuration": {},
}

// ProductTypeSnapshot is the authoritative type state at a type_revision.
type ProductTypeSnapshot struct {
	ProductTypeID string   `json:"product_type_id"`
	Code          string   `json:"code"`
	NameAR        string   `json:"name_ar"`
	NameEN        string   `json:"name_en"`
	DescriptionAR *string  `json:"description_ar,omitempty"`
	DescriptionEN *string  `json:"description_en,omitempty"`
	IsActive      bool     `json:"is_active"`
	Position      int      `json:"position"`
	Dimensions    []string `json:"dimensions"`
	Capabilities  []string `json:"capabilities"`
	TypeRevision  int64    `json:"type_revision"`
}

// DecodeProductTypeSnapshot parses canonical payload bytes. Decoding
// into int64 rejects float JSON money and overflow at parse time.
func DecodeProductTypeSnapshot(raw json.RawMessage) (ProductTypeSnapshot, error) {
	var p ProductTypeSnapshot
	if err := decodePayload(EventProductTypeSnapshotV1, raw, &p); err != nil {
		return ProductTypeSnapshot{}, err
	}
	return p, nil
}

// IsDerivedStorageIdentity reports whether id is an RFC 4122 version-5
// UUID. Cloud stores shared installation seeds under deterministic
// Store-scoped UUIDv5 keys; Retail mints every ProductType identity as a
// random v4 UUID (the fixed seed is v4 as well). A v5 ProductType identity
// in Retail intent can therefore only be an attempt to occupy another
// Store's derived storage key, and is refused (Phase 17-R3 F16).
func IsDerivedStorageIdentity(id string) bool {
	return isUUID(id) && id[14] == '5'
}

// ValidateProductTypeSnapshot enforces Retail-guaranteed invariants:
// UUID identity, stable code shape, bilingual names, non-negative
// position, positive revision, bounded dimension/capability sets with
// known capability codes only.
func ValidateProductTypeSnapshot(p ProductTypeSnapshot) (ProductTypeSnapshot, error) {
	fail := func(format string, args ...any) (ProductTypeSnapshot, error) {
		return ProductTypeSnapshot{}, apperr.New(apperr.InvalidInput, fmt.Sprintf(format, args...))
	}
	if !isUUID(p.ProductTypeID) {
		return fail("product_type_id must be a UUID")
	}
	if IsDerivedStorageIdentity(p.ProductTypeID) {
		return fail("product_type_id must not be a derived storage identity")
	}
	code := strings.TrimSpace(p.Code)
	if code == "" || len(code) > 32 || strings.ToLower(code) != code {
		return fail("code must be 1..32 lowercase [a-z0-9_]")
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return fail("code must be 1..32 lowercase [a-z0-9_]")
		}
	}
	p.Code = code
	if strings.TrimSpace(p.NameAR) == "" || strings.TrimSpace(p.NameEN) == "" {
		return fail("type names are required")
	}
	if p.Position < 0 {
		return fail("position must be >= 0")
	}
	if p.TypeRevision < 1 {
		return fail("type_revision must be >= 1")
	}
	if p.Dimensions == nil {
		p.Dimensions = []string{}
	}
	if p.Capabilities == nil {
		p.Capabilities = []string{}
	}
	seenDims := make(map[string]struct{}, len(p.Dimensions))
	for _, d := range p.Dimensions {
		d = strings.TrimSpace(d)
		if d == "" || len(d) > 64 {
			return fail("dimension codes must be 1..64 chars")
		}
		if _, dup := seenDims[d]; dup {
			return fail("duplicate dimension %q", d)
		}
		seenDims[d] = struct{}{}
	}
	seenCaps := make(map[string]struct{}, len(p.Capabilities))
	for _, c := range p.Capabilities {
		c = strings.TrimSpace(c)
		if _, ok := KnownCapabilityCodes[c]; !ok {
			return fail("unknown capability %q", c)
		}
		if _, dup := seenCaps[c]; dup {
			return fail("duplicate capability %q", c)
		}
		seenCaps[c] = struct{}{}
	}
	return p, nil
}

// NormalizedProductType is the comparison form of a type snapshot:
// sorted dimensions and capabilities plus all scalar state.
type NormalizedProductType struct {
	ProductTypeID string   `json:"product_type_id"`
	Code          string   `json:"code"`
	NameAR        string   `json:"name_ar"`
	NameEN        string   `json:"name_en"`
	DescriptionAR *string  `json:"description_ar,omitempty"`
	DescriptionEN *string  `json:"description_en,omitempty"`
	IsActive      bool     `json:"is_active"`
	Position      int      `json:"position"`
	Dimensions    []string `json:"dimensions"`
	Capabilities  []string `json:"capabilities"`
	Revision      int64    `json:"revision"`
}

// NormalizeProductTypeSnapshot sorts set fields for deterministic compare.
func NormalizeProductTypeSnapshot(p ProductTypeSnapshot) NormalizedProductType {
	dims := append([]string{}, p.Dimensions...)
	sort.Strings(dims)
	caps := append([]string{}, p.Capabilities...)
	sort.Strings(caps)
	return NormalizedProductType{
		ProductTypeID: p.ProductTypeID, Code: p.Code,
		NameAR: p.NameAR, NameEN: p.NameEN,
		DescriptionAR: p.DescriptionAR, DescriptionEN: p.DescriptionEN,
		IsActive: p.IsActive, Position: p.Position,
		Dimensions: dims, Capabilities: caps, Revision: p.TypeRevision,
	}
}

// FingerprintProductType returns the semantic identity of a type snapshot
// (stored source_payload_hash for type events).
func FingerprintProductType(p ProductTypeSnapshot) [32]byte {
	return fingerprint(NormalizeProductTypeSnapshot(p))
}
