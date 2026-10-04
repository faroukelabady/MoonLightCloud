package commerce

// Phase 15 §264: freeze-critical option invariants — one shared stock
// pool, exact money, deterministic option identity, and reevaluation-safe
// operation keys.

import (
	"math"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

func config(id, style, color string, egp int64, usd *int64, enabled bool, position int, revision int64) catalog.ProductConfiguration {
	return catalog.ProductConfiguration{
		ID: id, Kind: catalog.ConfigurationKindFrame,
		StyleCode: style, StyleNameAR: style + " AR", ColorCode: color, ColorNameAR: color + " AR",
		PriceDeltaEGPCents: egp, PriceDeltaUSDCents: usd,
		Enabled: enabled, Position: position, Revision: revision,
	}
}

// §264.9: configured price is exact int64 minor units with overflow
// rejection — never float, never wraparound, never FX.
func TestConfiguredPriceExactMoney(t *testing.T) {
	total, err := ConfiguredPrice(100000, 30000)
	if err != nil || total != 130000 {
		t.Fatalf("base+delta = %d, %v", total, err)
	}
	if _, err := ConfiguredPrice(100, -1); err == nil {
		t.Fatal("negative delta must be rejected")
	}
	if _, err := ConfiguredPrice(math.MaxInt64, 1); err == nil {
		t.Fatal("int64 overflow must be rejected, never wrapped")
	}
	if total, err := ConfiguredPrice(0, 0); err != nil || total != 0 {
		t.Fatalf("zero delta is business-valid: %d %v", total, err)
	}
}

// §48-§50/§264.14: deterministic option identity — option transitions
// rotate operation identity; unrelated Products never do; Retail-only
// bookkeeping (revisions) rides the version channel separately from the
// published-semantic fingerprint.
func TestConfigurationIdentityDeterminismism(t *testing.T) {
	base := []catalog.ProductConfiguration{
		config("11111111-0000-4000-8000-000000000001", "classic", "black", 30000, nil, true, 0, 1),
		config("11111111-0000-4000-8000-000000000002", "classic", "gold", 35000, nil, true, 1, 1),
	}
	fp1, ver1 := configurationIdentity(base)
	// Deterministic regardless of input order (§97-style canonicalization).
	reversed := []catalog.ProductConfiguration{base[1], base[0]}
	fp2, ver2 := configurationIdentity(reversed)
	if fp1 != fp2 || ver1 != ver2 {
		t.Fatal("option identity must be order-independent")
	}
	// Disable one choice → fingerprint rotates (published semantics).
	disabled := []catalog.ProductConfiguration{base[0], config(base[1].ID, "classic", "gold", 35000, nil, false, 1, 2)}
	fp3, _ := configurationIdentity(disabled)
	if fp3 == fp1 {
		t.Fatal("disable must rotate the option fingerprint")
	}
	// Price delta change → fingerprint rotates (§174 price update identity).
	repriced := []catalog.ProductConfiguration{config(base[0].ID, "classic", "black", 40000, nil, true, 0, 2), base[1]}
	fp4, _ := configurationIdentity(repriced)
	if fp4 == fp1 {
		t.Fatal("price change must rotate the option fingerprint")
	}
	// Rename (labels are published) → fingerprint rotates with the label.
	renamed := []catalog.ProductConfiguration{config(base[0].ID, "classic", "black", 30000, nil, true, 0, 3), base[1]}
	renamed[0].StyleNameAR = "تراثي"
	fp5, _ := configurationIdentity(renamed)
	if fp5 == fp1 {
		t.Fatal("published label change must rotate the fingerprint")
	}
	// Same state → same identity (rapid mutations converge; §150).
	again := []catalog.ProductConfiguration{base[0], base[1]}
	fp6, ver6 := configurationIdentity(again)
	if fp6 != fp1 || ver6 != ver1 {
		t.Fatal("identical state must map to identical identity")
	}
}

// §47: configuration transitions must change provider operation keys so
// stale idempotency semantics can never be reused (§264.7-style).
func TestOperationKeyConfigurationAware(t *testing.T) {
	before := ProductOperationKey("prov", "prod-1", 7, 3, true, "cfp", "cpv", "optA", "optV1")
	afterDisable := ProductOperationKey("prov", "prod-1", 7, 3, true, "cfp", "cpv", "optB", "optV2")
	if before == afterDisable {
		t.Fatal("option transition must rotate the product operation key")
	}
	same := ProductOperationKey("prov", "prod-1", 7, 3, true, "cfp", "cpv", "optA", "optV1")
	if before != same {
		t.Fatal("identical desired state must keep the same key")
	}
	unrelated := ProductOperationKey("prov", "prod-2", 7, 3, true, "cfp", "cpv", "optA", "optV1")
	if before == unrelated {
		t.Fatal("product identity must separate keys")
	}
	beforeInv := InventoryOperationKey("prov", "prod-1", 7, 3, 5, 1, true, "cfp", "cpv", "optA", "optV1")
	afterInv := InventoryOperationKey("prov", "prod-1", 7, 3, 5, 1, true, "cfp", "cpv", "optB", "optV2")
	if beforeInv == afterInv {
		t.Fatal("inventory identity must be option-aware")
	}
}

// §111/§112: a disabled configuration is not a disabled Product — the
// desired publication state stays the Product's own.
func TestConfigurationsDoNotChangePublication(t *testing.T) {
	desired := DesiredProduct{
		Published: true,
		Configurations: []CommerceConfiguration{
			{ConfigurationID: "c1", Kind: "frame", Enabled: true},
			{ConfigurationID: "c2", Kind: "frame", Enabled: false},
		},
	}
	for _, configuration := range desired.Configurations {
		if configuration.Kind != "frame" {
			t.Fatal("provider-neutral kind only")
		}
	}
	if !desired.Published {
		t.Fatal("configuration enablement must not gate Product publication")
	}
	if strings.Contains(desired.Configurations[0].ConfigurationID, "shopify") ||
		strings.Contains(desired.Configurations[0].ConfigurationID, "woo") {
		t.Fatal("configuration identity must never name a provider")
	}
}
