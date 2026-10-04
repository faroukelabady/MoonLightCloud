package woocommerce

// Phase 15 §84-§92: WooCommerce frame-configuration representation.
//
// Verified semantics (woocommerce.com/document/variable-product/):
// "Product level inventory management sets a stock quantity that all
// variations can share" — variations with manage_stock=false pull from
// the parent-level pool. One variable Product therefore preserves ONE
// physical papyrus inventory across No Frame and every frame choice
// (§163): the parent carries the canonical OnlineAvailable; variations
// never carry quantities.
//
// Ownership is proven by `_moonlight_configuration_id` metadata (§62)
// — never by style/color labels (§66/§170): foreign or manually created
// variations are left untouched. Mappings persist through disable/
// re-enable (§63); ambiguous creates recover the owned variation (§65).

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

const (
	metaConfigurationID = "_moonlight_configuration_id"
	wooTypeVariable     = "variable"
	frameStyleAttribute = "Frame Style"
	frameColorAttribute = "Frame Color"
	noFrameOption       = "No Frame"
)

// syncWooVariations converges one variable Product's variation set to
// the desired configuration state. Returns the owned variation identity
// per configuration ("" for the implicit NO-FRAME choice).
func (p *WooCommerceProvider) syncWooVariations(ctx context.Context, externalProductID string, product commerce.CommerceProduct) (map[string]string, error) {
	existing, err := p.listOwnedVariations(ctx, externalProductID)
	if err != nil {
		return nil, err
	}
	identities := make(map[string]string, len(product.Configurations)+1)
	// Implicit NO-FRAME choice (§18/§86): an integration representation
	// only — no MoonLight configuration ID exists for it.
	if err := p.convergeVariation(ctx, externalProductID, existing, commerce.NoFrameConfigurationID, "", "", noFrameOption, noFrameOption, product, true, 0, identities); err != nil {
		return nil, err
	}
	configurations := append([]commerce.CommerceConfiguration(nil), product.Configurations...)
	sort.SliceStable(configurations, func(i, j int) bool {
		if configurations[i].Position != configurations[j].Position {
			return configurations[i].Position < configurations[j].Position
		}
		return configurations[i].ConfigurationID < configurations[j].ConfigurationID
	})
	for _, configuration := range configurations {
		styleLabel := displayLabel(configuration.StyleNameEN, configuration.StyleNameAR)
		colorLabel := displayLabel(configuration.ColorNameEN, configuration.ColorNameAR)
		delta, available := resolveDelta(configuration, p.currency)
		// A configuration without a coherent price for this currency is
		// never published (§183) — its remote representation stays hidden.
		enabled := configuration.Enabled && available
		if err := p.convergeVariation(ctx, externalProductID, existing, configuration.ConfigurationID,
			configuration.StyleCode, configuration.ColorCode, styleLabel, colorLabel,
			product, enabled, delta, identities); err != nil {
			return nil, err
		}
	}
	return identities, nil
}

// convergeVariation creates/updates one owned variation. The NO-FRAME
// choice always stays enabled; disabled configurations are hidden by
// withholding their price (official Woo behavior) with mapping retained.
func (p *WooCommerceProvider) convergeVariation(ctx context.Context, externalProductID string, existing map[string]wooVariation,
	configurationID, styleCode, colorCode, styleLabel, colorLabel string,
	product commerce.CommerceProduct, enabled bool, delta int64, identities map[string]string) error {
	price := ""
	if enabled {
		configured, err := configuredPrice(product, p.currency, delta)
		if err != nil {
			return err
		}
		price = configured
	}
	payload := wooVariationPayload{
		Attributes: []wooVariationAttribute{
			{Name: frameStyleAttribute, Option: styleLabel},
			{Name: frameColorAttribute, Option: colorLabel},
		},
		RegularPrice: price,
		ManageStock:  false, // shared parent pool (§85) — never per-variation stock
		MetaData: []wooMetaDatum{
			{Key: metaConfigurationID, Value: configurationID},
			{Key: metaProductID, Value: product.ProductID},
			{Key: metaProviderKey, Value: string(p.key)},
		},
	}
	if current, ok := existing[configurationID]; ok {
		identities[configurationID] = strconv.FormatInt(current.ID, 10)
		_, err := p.client.do(ctx, http.MethodPut, "/products/"+externalProductID+"/variations/"+strconv.FormatInt(current.ID, 10), nil, payload, nil)
		return err
	}
	// Ambiguous-create recovery (§65/§92): a create whose response was
	// lost re-adopts the owned variation on retry — never a duplicate.
	created, err := p.createVariation(ctx, externalProductID, payload)
	if err != nil {
		return err
	}
	identities[configurationID] = strconv.FormatInt(created, 10)
	return nil
}

func (p *WooCommerceProvider) createVariation(ctx context.Context, externalProductID string, payload wooVariationPayload) (int64, error) {
	var result struct {
		ID int64 `json:"id"`
	}
	_, err := p.client.do(ctx, http.MethodPost, "/products/"+externalProductID+"/variations", nil, payload, &result)
	if err != nil {
		// Lost-response recovery: the remote may already own the
		// variation. Search by ownership before reporting failure.
		if owned, findErr := p.listOwnedVariations(ctx, externalProductID); findErr == nil {
			for _, variation := range owned {
				if variationConfigurationID(variation) == metaValue(payload.MetaData, metaConfigurationID) {
					return variation.ID, nil
				}
			}
		}
		return 0, err
	}
	if result.ID == 0 {
		return 0, fmt.Errorf("woo variation create: missing identity")
	}
	return result.ID, nil
}

// listOwnedVariations returns MoonLight-owned variations indexed by
// configuration identity. Foreign/manual variations are ignored (§89).
func (p *WooCommerceProvider) listOwnedVariations(ctx context.Context, externalProductID string) (map[string]wooVariation, error) {
	owned := make(map[string]wooVariation)
	for page := 1; page <= 4; page++ {
		var batch []wooVariation
		path := "/products/" + externalProductID + "/variations?per_page=100&page=" + strconv.Itoa(page)
		if _, err := p.client.do(ctx, http.MethodGet, path, nil, nil, &batch); err != nil {
			return nil, err
		}
		for _, variation := range batch {
			if configurationID := variationConfigurationID(variation); configurationID != "" {
				owned[configurationID] = variation
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	return owned, nil
}

func variationConfigurationID(variation wooVariation) string {
	return metaValue(variation.MetaData, metaConfigurationID)
}

func metaValue(meta []wooMetaDatum, key string) string {
	for _, entry := range meta {
		if entry.Key == key {
			return entry.Value
		}
	}
	return ""
}

func displayLabel(en *string, ar string) string {
	if en != nil && *en != "" {
		return *en
	}
	return ar
}

// resolveDelta resolves the delta for the adapter currency (§41/§183):
// a configuration missing its currency delta is NOT published in that
// currency; no FX is ever inferred (§42). ok=false means unavailable.
func resolveDelta(configuration commerce.CommerceConfiguration, currency string) (int64, bool) {
	if strings.EqualFold(currency, "USD") {
		if configuration.PriceDeltaUSDMinor == nil {
			return 0, false
		}
		return *configuration.PriceDeltaUSDMinor, true
	}
	return configuration.PriceDeltaEGPMinor, true
}

// configuredPrice formats base + delta as the exact decimal Woo price
// for the adapter currency (§87).
func configuredPrice(product commerce.CommerceProduct, currency string, delta int64) (string, error) {
	base := int64(0)
	found := false
	for _, price := range product.Prices {
		if strings.EqualFold(price.Currency, currency) {
			base = price.AmountMinor
			found = true
			break
		}
	}
	if !found {
		return "", commerce.ValidationError(fmt.Sprintf("product has no %s price", currency))
	}
	total, err := commerce.ConfiguredPrice(base, delta)
	if err != nil {
		return "", err
	}
	return minorToDecimal(total)
}
