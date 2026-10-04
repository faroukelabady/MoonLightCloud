package catalog

// Phase 15 — Product configuration snapshot contract (Retail-authoritative
// ONLINE product options). Wire mirror of MoonLightRetail
// internal/domain/sync/catalog_configurations.go. The Product snapshot
// wire contract is untouched. Current-state only: online orders carry
// their own immutable selection snapshots (§55/§56).

import (
	"encoding/json"
	"fmt"
	"sort"
)

// EventProductConfigurationSnapshotV1 is the complete ONLINE product
// option (frame configuration) state of one Product at one Product
// configuration revision.
const EventProductConfigurationSnapshotV1 = "catalog.product.configuration.snapshot.v1"

// Configuration kinds (Phase 15 supports the first only).
const ConfigurationKindFrame = "frame"

// ProductConfigurationEntry is one valid configuration (a valid
// combination row — never a Cartesian generation).
type ProductConfigurationEntry struct {
	ConfigurationID       string  `json:"configuration_id"`
	Kind                  string  `json:"kind"`
	StyleCode             string  `json:"style_code"`
	StyleNameAR           string  `json:"style_name_ar"`
	StyleNameEN           *string `json:"style_name_en,omitempty"`
	ColorCode             string  `json:"color_code"`
	ColorNameAR           string  `json:"color_name_ar"`
	ColorNameEN           *string `json:"color_name_en,omitempty"`
	PriceDeltaEGPCents    int64   `json:"price_delta_egp_cents"`
	PriceDeltaUSDCents    *int64  `json:"price_delta_usd_cents,omitempty"`
	Enabled               bool    `json:"enabled"`
	Position              int     `json:"position"`
	ConfigurationRevision int64   `json:"configuration_revision"`
}

// ProductConfigurationsSnapshot is the complete configuration state of
// one Product at one aggregate configuration revision.
type ProductConfigurationsSnapshot struct {
	ProductID             string                      `json:"product_id"`
	Configurations        []ProductConfigurationEntry `json:"configurations"`
	ConfigurationRevision int64                       `json:"configuration_revision"`
}

// DecodeProductConfigurationsSnapshot parses canonical payload bytes.
func DecodeProductConfigurationsSnapshot(raw json.RawMessage) (ProductConfigurationsSnapshot, error) {
	var snapshot ProductConfigurationsSnapshot
	if err := decodePayload(EventProductConfigurationSnapshotV1, raw, &snapshot); err != nil {
		return ProductConfigurationsSnapshot{}, err
	}
	return snapshot, nil
}

// ValidateProductConfigurationsSnapshot enforces the Phase 15 contract
// bounds (§29/§30/§37-§42/§166): stable identity, bounded labels/codes,
// exact non-negative minor units (no floats anywhere), unique
// style_code+color_code per Product, explicit rows only.
func ValidateProductConfigurationsSnapshot(snapshot ProductConfigurationsSnapshot) (ProductConfigurationsSnapshot, error) {
	if !isUUID(snapshot.ProductID) {
		return snapshot, fmt.Errorf("invalid %s: product_id must be a UUID", EventProductConfigurationSnapshotV1)
	}
	if snapshot.ConfigurationRevision < 1 {
		return snapshot, fmt.Errorf("invalid %s: configuration revision must be >= 1", EventProductConfigurationSnapshotV1)
	}
	if len(snapshot.Configurations) > 100 {
		return snapshot, fmt.Errorf("invalid %s: too many configurations", EventProductConfigurationSnapshotV1)
	}
	seen := make(map[string]bool, len(snapshot.Configurations))
	for _, entry := range snapshot.Configurations {
		if !isUUID(entry.ConfigurationID) {
			return snapshot, fmt.Errorf("invalid %s: configuration_id must be a UUID", EventProductConfigurationSnapshotV1)
		}
		if entry.Kind != ConfigurationKindFrame {
			return snapshot, fmt.Errorf("invalid %s: unsupported configuration kind", EventProductConfigurationSnapshotV1)
		}
		if len(entry.StyleCode) < 1 || len(entry.StyleCode) > 32 || len(entry.ColorCode) < 1 || len(entry.ColorCode) > 32 {
			return snapshot, fmt.Errorf("invalid %s: configuration code out of bounds", EventProductConfigurationSnapshotV1)
		}
		if err := validateConfigurationLabel(entry.StyleNameAR); err != nil {
			return snapshot, err
		}
		if err := validateConfigurationLabel(entry.ColorNameAR); err != nil {
			return snapshot, err
		}
		if entry.StyleNameEN != nil && validateConfigurationLabel(*entry.StyleNameEN) != nil {
			return snapshot, fmt.Errorf("invalid %s: label out of bounds", EventProductConfigurationSnapshotV1)
		}
		if entry.ColorNameEN != nil && validateConfigurationLabel(*entry.ColorNameEN) != nil {
			return snapshot, fmt.Errorf("invalid %s: label out of bounds", EventProductConfigurationSnapshotV1)
		}
		if entry.PriceDeltaEGPCents < 0 || (entry.PriceDeltaUSDCents != nil && *entry.PriceDeltaUSDCents < 0) {
			return snapshot, fmt.Errorf("invalid %s: negative price delta", EventProductConfigurationSnapshotV1)
		}
		if entry.Position < 0 || entry.ConfigurationRevision < 1 {
			return snapshot, fmt.Errorf("invalid %s: invalid position or revision", EventProductConfigurationSnapshotV1)
		}
		key := entry.StyleCode + "\x00" + entry.ColorCode
		if seen[key] {
			return snapshot, fmt.Errorf("invalid %s: duplicate style/color combination", EventProductConfigurationSnapshotV1)
		}
		seen[key] = true
	}
	return snapshot, nil
}

func validateConfigurationLabel(value string) error {
	runes := []rune(value)
	if len(runes) < 1 || len(runes) > 100 {
		return fmt.Errorf("invalid %s: label out of bounds", EventProductConfigurationSnapshotV1)
	}
	return nil
}

// NormalizedProductConfigurations is comparison form for equal-revision
// conflict detection (§45): semantic state only, canonical order.
type NormalizedProductConfigurations struct {
	ProductID             string                      `json:"product_id"`
	Configurations        []ProductConfigurationEntry `json:"configurations"`
	ConfigurationRevision int64                       `json:"configuration_revision"`
}

// NormalizeProductConfigurations reduces a validated snapshot to
// comparison form (deterministic order: position, then configuration id).
func NormalizeProductConfigurations(snapshot ProductConfigurationsSnapshot) NormalizedProductConfigurations {
	entries := append([]ProductConfigurationEntry(nil), snapshot.Configurations...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Position != entries[j].Position {
			return entries[i].Position < entries[j].Position
		}
		return entries[i].ConfigurationID < entries[j].ConfigurationID
	})
	return NormalizedProductConfigurations{
		ProductID: snapshot.ProductID, Configurations: entries,
		ConfigurationRevision: snapshot.ConfigurationRevision,
	}
}

// ProductConfiguration is the catalog read model of one projected
// configuration (Phase 15), used by commerce assembly.
type ProductConfiguration struct {
	ID                 string
	Kind               string
	StyleCode          string
	StyleNameAR        string
	StyleNameEN        *string
	ColorCode          string
	ColorNameAR        string
	ColorNameEN        *string
	PriceDeltaEGPCents int64
	PriceDeltaUSDCents *int64
	Enabled            bool
	Position           int
	Revision           int64
}
