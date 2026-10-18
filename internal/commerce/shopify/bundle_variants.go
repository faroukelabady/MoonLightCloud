package shopify

import (
	"context"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// configurationIdentities resolves selection identity from VARIANT
// METADATA (F08) — never from displayed titles. Labels may be equal,
// renamed or missing without merging or losing identity; foreign/manual
// variants without our metadata are never adopted (§104).
func configurationIdentities(variants []gqlVariant) map[string]string {
	identities := make(map[string]string, len(variants))
	for _, variant := range variants {
		configurationID := metafieldValue(variant.Metafields.Nodes, keyConfigurationIDMeta)
		if configurationID == "" {
			continue
		}
		identities[configurationID] = variant.ID
	}
	return identities
}

// chooseConfigurations filters the customer choices (F09): disabled and
// currency-incomplete configurations are never offered; NO-FRAME stays
// the coherent base-price choice.
func chooseConfigurations(product commerce.CommerceProduct, currency string) []commerce.CommerceConfiguration {
	choices := make([]commerce.CommerceConfiguration, 0, len(product.Configurations))
	for _, configuration := range product.Configurations {
		if !configuration.Enabled {
			continue
		}
		if _, ok := resolveDelta(configuration, currency); !ok {
			continue
		}
		choices = append(choices, configuration)
	}
	return choices
}

// bundleVariantIdentities loads the bundle variants and maps identity
// from stable metadata (F08).
// establishBundleVariantIdentity establishes and CONFIRMS the
// ownership-proven configuration identity of every sellable bundle
// variant (F08). Establishment matches the collision-disambiguated
// explicit combination tuple exactly once — never labels alone, never
// array position, never arbitrary first-match — and stamps the
// configuration_id metadata (including the NO-FRAME sentinel). All later
// reads use that metadata, so renames/reorders never change identity.
// An empty or partial map with intended choices is a bounded failure.
func (p *ShopifyProvider) establishBundleVariantIdentity(ctx context.Context, bundleProductID string, req commerce.ProductUpsertRequest, choices []commerce.CommerceConfiguration) (map[string]string, error) {
	state, err := p.loadProduct(ctx, bundleProductID)
	if err != nil {
		return nil, err
	}
	if state == nil || !ownershipMatches(ownership(state.Metafields.Nodes), req.ProductID, req.ProviderKey) || metafieldValue(state.Metafields.Nodes, keyRole) != roleBundleParent {
		return nil, p.capabilityError("bundle ownership mismatch")
	}
	baseID := metafieldValue(state.Metafields.Nodes, keyBaseComponentID)
	frameID := metafieldValue(state.Metafields.Nodes, keyFrameComponentID)
	if baseID == "" || frameID == "" {
		return nil, p.capabilityError("bundle component ownership missing")
	}
	base, err := p.loadProduct(ctx, baseID)
	if err != nil {
		return nil, err
	}
	frame, err := p.loadProduct(ctx, frameID)
	if err != nil {
		return nil, err
	}
	if base == nil || frame == nil || !ownershipMatches(ownership(base.Metafields.Nodes), req.ProductID, req.ProviderKey) || !ownershipMatches(ownership(frame.Metafields.Nodes), req.ProductID, req.ProviderKey) || metafieldValue(frame.Metafields.Nodes, keyRole) != roleFrameComponent {
		return nil, p.capabilityError("component ownership mismatch")
	}
	managed, err := managedVariant(base, req.Product.SKU, metafieldValue(base.Metafields.Nodes, metafieldManagedVariantID))
	if err != nil {
		return nil, err
	}
	frameIdentities := map[string]string{}
	for _, v := range frame.Variants.Nodes {
		id := metafieldValue(v.Metafields.Nodes, keyConfigurationIDMeta)
		if id == "" || frameIdentities[v.ID] != "" {
			return nil, p.capabilityError("frame variant identity missing")
		}
		frameIdentities[v.ID] = id
	}
	expected := map[string]bool{commerce.NoFrameConfigurationID: true}
	for _, choice := range choices {
		if expected[choice.ConfigurationID] {
			return nil, p.capabilityError("duplicate intended configuration")
		}
		expected[choice.ConfigurationID] = true
	}
	identities := map[string]string{}
	var stamps []map[string]any
	for _, variant := range state.Variants.Nodes {
		if variant.Components.PageInfo.HasNextPage || len(variant.Components.Nodes) != 2 {
			return nil, p.capabilityError("bundle component relationship incomplete")
		}
		configurationID := ""
		baseFound := false
		for _, component := range variant.Components.Nodes {
			if component.Quantity != 1 {
				return nil, p.capabilityError("bundle component quantity mismatch")
			}
			if component.Variant.Product.ID == baseID && component.Variant.ID == managed.ID && !baseFound {
				baseFound = true
				continue
			}
			if component.Variant.Product.ID != frameID || configurationID != "" {
				return nil, p.capabilityError("foreign bundle component")
			}
			configurationID = frameIdentities[component.Variant.ID]
		}
		stamped := metafieldValue(variant.Metafields.Nodes, keyConfigurationIDMeta)
		if !baseFound || !expected[configurationID] || identities[configurationID] != "" || (stamped != "" && stamped != configurationID) {
			return nil, p.capabilityError("ambiguous bundle variant identity")
		}
		identities[configurationID] = variant.ID
		if stamped == "" {
			stamps = append(stamps, map[string]any{"id": variant.ID, "metafields": []map[string]any{{"namespace": metafieldNamespace, "key": keyConfigurationIDMeta, "value": configurationID, "type": "single_line_text_field"}}})
		}
	}
	if len(identities) != len(expected) {
		return nil, p.capabilityError("bundle variant identity incomplete")
	}
	if len(stamps) > 0 {
		var out struct {
			ProductVariantsBulkUpdate struct {
				UserErrors []struct {
					Message string `json:"message"`
				} `json:"userErrors"`
			} `json:"productVariantsBulkUpdate"`
		}
		if err := p.client.do(ctx, docVariantPrices, map[string]any{
			"productId": bundleProductID, "variants": stamps,
		}, &out); err != nil {
			return nil, err
		}
		if len(out.ProductVariantsBulkUpdate.UserErrors) > 0 {
			return nil, commerce.ValidationError(p.client.safeMessage(out.ProductVariantsBulkUpdate.UserErrors[0].Message))
		}
	}
	return identities, nil
}

func (p *ShopifyProvider) bundleVariantIdentities(ctx context.Context, bundleProductID string, product commerce.CommerceProduct) (map[string]string, error) {
	state, err := p.loadProduct(ctx, bundleProductID)
	if err != nil {
		return nil, err
	}
	return configurationIdentities(state.Variants.Nodes), nil
}

// setBundleVariantPrices writes exact configured prices on the bundle
// variants whose identity is configuration-metadata proven (F08). Nested
// userErrors are decoded and never ignored (F10): a refusal is a bounded
// failure, never a silent success.
func (p *ShopifyProvider) setBundleVariantPrices(ctx context.Context, bundleProductID string, product commerce.CommerceProduct, identities map[string]string, choices []commerce.CommerceConfiguration) error {
	base, err := effectiveProductPrice(product, p.currency)
	if err != nil {
		return err
	}
	baseMinor, err := ParseMoneyString(base, p.currency)
	if err != nil {
		return err
	}
	type variantPrice struct {
		ID    string `json:"id"`
		Price string `json:"price"`
	}
	var updates []variantPrice
	if id := identities[commerce.NoFrameConfigurationID]; id != "" {
		updates = append(updates, variantPrice{ID: id, Price: base})
	}
	for _, configuration := range choices {
		id := identities[configuration.ConfigurationID]
		if id == "" {
			continue
		}
		delta, ok := resolveDelta(configuration, p.currency)
		if !ok {
			continue // never offered in this currency (F09)
		}
		total, err := commerce.ConfiguredPrice(baseMinor, delta)
		if err != nil {
			return err
		}
		formatted, err := FormatMinorUnits(total)
		if err != nil {
			return err
		}
		updates = append(updates, variantPrice{ID: id, Price: formatted})
	}
	if len(updates) == 0 {
		return nil
	}
	var out struct {
		ProductVariantsBulkUpdate struct {
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"productVariantsBulkUpdate"`
	}
	if err := p.client.do(ctx, docVariantPrices, map[string]any{
		"productId": bundleProductID, "variants": updates,
	}, &out); err != nil {
		return err
	}
	// F10: nested mutation userErrors must surface as bounded failures.
	if len(out.ProductVariantsBulkUpdate.UserErrors) > 0 {
		return commerce.ValidationError(p.client.safeMessage("bundle price refusal: " + out.ProductVariantsBulkUpdate.UserErrors[0].Message))
	}
	return nil
}

// frameComponentOptions builds bundle optionSelections carrying the
// component option IDENTIFIERS (F06): OptionSetInput values are
// option-value objects and ProductBundleComponentOptionSelectionInput
// requires componentOptionId — never bare strings, never labels as
// identity (F08).
func frameComponentOptions(optionIDs map[string]string, choices []commerce.CommerceConfiguration) []map[string]any {
	return []map[string]any{
		{"componentOptionId": optionIDs["Frame Style"], "name": "Frame Style", "values": frameStyleValues(choices)},
		{"componentOptionId": optionIDs["Frame Color"], "name": "Frame Color", "values": frameColorValues(choices)},
	}
}

// optionValueList renders OptionSetInput values (objects, F06).
func optionValueList(values []string) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		out = append(out, map[string]any{"name": value})
	}
	return out
}

// frameComponentVariants lists EXPLICIT valid combinations (§13) as
// untracked variants (§77: frames carry no stock). Disabled and
// currency-incomplete choices are never created (F09).
func frameComponentVariants(choices []commerce.CommerceConfiguration) []map[string]any {
	variants := []map[string]any{
		{"optionValues": []map[string]any{
			{"optionName": "Frame Style", "name": noFrameVariantName},
			{"optionName": "Frame Color", "name": noFrameVariantName},
		}, "inventoryItem": map[string]any{"tracked": false}, "inventoryPolicy": "CONTINUE", "metafields": []map[string]any{{"namespace": metafieldNamespace, "key": keyConfigurationIDMeta, "value": commerce.NoFrameConfigurationID, "type": "single_line_text_field"}}},
	}
	styles, colors := frameStyleValues(choices), frameColorValues(choices)
	for index, configuration := range choices {
		variants = append(variants, map[string]any{
			"optionValues": []map[string]any{
				{"optionName": "Frame Style", "name": styles[index+1]},
				{"optionName": "Frame Color", "name": colors[index+1]},
			},
			"inventoryItem":   map[string]any{"tracked": false},
			"inventoryPolicy": "CONTINUE",
			"price":           "0.00",
			"metafields": []map[string]any{
				{"namespace": metafieldNamespace, "key": keyConfigurationIDMeta, "value": configuration.ConfigurationID, "type": "single_line_text_field"},
			},
		})
	}
	return variants
}

// resolveDelta resolves the delta for the adapter currency (§41/§183).
func resolveDelta(configuration commerce.CommerceConfiguration, currency string) (int64, bool) {
	if strings.EqualFold(currency, "USD") {
		if configuration.PriceDeltaUSDMinor == nil {
			return 0, false
		}
		return *configuration.PriceDeltaUSDMinor, true
	}
	return configuration.PriceDeltaEGPMinor, true
}

func displayLabel(en *string, ar string) string {
	if en != nil && strings.TrimSpace(*en) != "" {
		return *en
	}
	return ar
}

func frameComponentTitle(product commerce.CommerceProduct) string {
	for _, name := range product.Names {
		if strings.EqualFold(name.Locale, "ar") && strings.TrimSpace(name.Name) != "" {
			return name.Name + " (إطار)"
		}
	}
	return product.SKU + " Frame"
}

func bundleTitle(product commerce.CommerceProduct) string {
	for _, name := range product.Names {
		if strings.TrimSpace(name.Name) != "" {
			return name.Name
		}
	}
	return product.SKU
}

// frameStyleValues builds the DISTINCT customer-facing option values.
// Equal display labels never collapse distinct identities (F08): a
// colliding label is deterministically disambiguated with its stable
// machine code (identity-backed text, never an arbitrary guess).
func frameStyleValues(choices []commerce.CommerceConfiguration) []string {
	values := []string{noFrameVariantName}
	for _, c := range choices {
		values = append(values, displayLabel(c.StyleNameEN, c.StyleNameAR)+" ("+c.ConfigurationID+")")
	}
	return values
}

func frameColorValues(choices []commerce.CommerceConfiguration) []string {
	values := []string{noFrameVariantName}
	for _, c := range choices {
		values = append(values, displayLabel(c.ColorNameEN, c.ColorNameAR)+" ("+c.ConfigurationID+")")
	}
	return values
}

// componentMetafields are inline productSet metafields carrying the
// frozen MoonLight ownership pattern (F06/F100: role + identity).
func componentMetafields(req commerce.ProductUpsertRequest, role string) []map[string]any {
	return []map[string]any{
		{"namespace": metafieldNamespace, "key": metafieldProductID, "value": req.ProductID, "type": "single_line_text_field"},
		{"namespace": metafieldNamespace, "key": metafieldProviderKey, "value": string(req.ProviderKey), "type": "single_line_text_field"},
		{"namespace": metafieldNamespace, "key": keyRole, "value": role, "type": "single_line_text_field"},
		{"namespace": metafieldNamespace, "key": "product_operation_key", "value": req.OperationKey, "type": "single_line_text_field"},
	}
}

// Select the owned managed base variant's actual option values. Labels are
// copied only as provider selection values, never guessed as identities.
func (p *ShopifyProvider) baseComponentOptions(ctx context.Context, productID, sku string) ([]map[string]any, error) {
	base, err := p.loadProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, p.capabilityError("base component missing")
	}
	var variant gqlVariant
	if sku != "" {
		variant, err = managedVariant(base, sku, metafieldValue(base.Metafields.Nodes, metafieldManagedVariantID))
	} else {
		variant, err = recordedVariant(base, metafieldValue(base.Metafields.Nodes, metafieldManagedVariantID))
	}
	if err != nil {
		return nil, err
	}
	options := make([]map[string]any, 0, len(base.Options))
	for _, option := range base.Options {
		value := ""
		for _, selection := range variant.SelectedOptions {
			if selection.Name == option.Name {
				value = selection.Value
			}
		}
		if option.ID == "" || value == "" {
			return nil, p.capabilityError("base option selection missing")
		}
		options = append(options, map[string]any{"componentOptionId": option.ID, "name": option.Name, "values": []string{value}})
	}
	if len(options) == 0 {
		return nil, p.capabilityError("base options missing")
	}
	return options, nil
}

func (p *ShopifyProvider) resumePendingBundleWork(ctx context.Context) error {
	store, err := commerce.ProductAsyncReceipts(ctx)
	if err != nil {
		return err
	}
	for _, role := range []string{"bundle_create", "bundle_update"} {
		receipt, found, err := store.LoadAsync(ctx, role)
		if err != nil {
			return err
		}
		if found && receipt.State == "pending" {
			if _, err := p.awaitBundleOperation(ctx, receipt.OperationID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStampedVariants(product *gqlProduct) error {
	if product == nil {
		return commerce.ConflictError("bundle missing")
	}
	seen := map[string]bool{}
	for _, variant := range product.Variants.Nodes {
		id := metafieldValue(variant.Metafields.Nodes, keyConfigurationIDMeta)
		if id != "" {
			if seen[id] {
				return commerce.ConflictError("ambiguous bundle configuration identity")
			}
			seen[id] = true
		}
	}
	return nil
}
