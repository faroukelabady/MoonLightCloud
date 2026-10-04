package shopify

// Phase 15 §93-§98: Shopify frame-configuration representation.
//
// Verified semantics (shopify.dev/docs/apps/build/product-merchandising/
// bundles, productBundleCreate/productBundleUpdate, pinned API):
// "The bundle parent variant's price determines the price, while the
// inventory of each component's variants determines the bundle
// inventory."
//
// Strategy: the sellable remote Product is a BUNDLE whose components are
// (1) the base papyrus Product — the ONLY tracked-inventory component,
// its managed variant carrying the canonical OnlineAvailable — and (2) an
// untracked "frame" component Product whose variants are the valid
// frame combinations plus NO-FRAME. Because bundle inventory derives
// from the tracked component, all customer choices consume ONE base
// papyrus pool (§97 prohibition satisfied: native parallel variants each
// owning the full quantity are NEVER used).
//
// Identity: the base Product keeps its remote identity as the component
// (its inventory item is never migrated); the bundle parent becomes the
// mapped sellable identity with a documented simple->framed mapping
// transition (§116). Configuration identity is the bundle-parent variant
// GID, stored in durable configuration mappings — never label-matched
// (§62). Required scope: write_products (bundle mutations are part of
// the product surface). Capability gate (§96): if the shop refuses the
// bundle surface, the sync reports the stable
// SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE state instead of
// approximating with unsafe native variants (§216).

import (
	"context"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// CapabilityCode is the stable blocked-state code returned when the
// configured shop cannot represent frame options safely (§96).
const CapabilityCode = "SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE"

// bundleDocuments are the GraphQL operations for the bundle surface.
const (
	docBundleCreate = `mutation MoonlightBundleCreate($input: ProductBundleCreateInput!) {
  productBundleCreate(input: $input) {
    productBundleOperation { id status }
    userErrors { message field }
  }
}`
	docBundleUpdate = `mutation MoonlightBundleUpdate($input: ProductBundleUpdateInput!) {
  productBundleUpdate(input: $input) {
    productBundleOperation { id status }
    userErrors { message field }
  }
}`
	docBundleOperation = `query MoonlightBundleOperation($id: ID!) {
  productOperation(id: $id) {
    ... on ProductBundleOperation { id status product { id } userErrors { field message code } }
  }
}`
	docBundleComponents = `query MoonlightBundleComponents($id: ID!) {
  product(id: $id) {
    id
    bundleComponents(first: 10) { nodes { id quantity optionSelections { componentOptionId name values } } }
  }
}`
	docVariantPrices = `mutation MoonlightVariantPrices($productId: ID!, $variants: [ProductVariantsBulkInput!]!) {
  productVariantsBulkUpdate(productId: $productId, variants: $variants) {
    productVariants { id price }
    userErrors { field message }
  }
}`
	docFrameComponentSet = `mutation MoonlightFrameComponentSet($input: ProductSetInput!) {
  productSet(synchronous: true, input: $input) {
    product { id variants(first: 100) { nodes { id name } } }
    userErrors { field message }
  }
}`
)

// capabilityError is the stable provider error for shops without the
// bundle surface. It is a validation-class failure: deterministic, no
// retry storm, no unsafe fallback.
func capabilityError(detail string) error {
	return commerce.ValidationError(CapabilityCode + ": " + detail)
}

// syncShopifyConfigurations converges one framed Product's bundle
// representation. The returned identities map MoonLight configuration
// IDs (plus NoFrameConfigurationID) to bundle-parent variant GIDs.
func (p *ShopifyProvider) syncShopifyConfigurations(ctx context.Context, req commerce.ProductUpsertRequest, bundleProductID string) (map[string]string, error) {
	frameComponentID, err := p.ensureFrameComponent(ctx, req)
	if err != nil {
		return nil, err
	}
	baseComponentID, err := p.baseComponentID(ctx, req, bundleProductID)
	if err != nil {
		return nil, err
	}
	if err := p.ensureBundleComponents(ctx, bundleProductID, baseComponentID, frameComponentID, req.Product); err != nil {
		return nil, err
	}
	return p.bundleVariantIdentities(ctx, req, bundleProductID)
}

// ensureFrameComponent creates/updates the untracked frame component
// Product (variants = NO-FRAME + each valid configuration; untracked
// inventory — frames carry no stock in Phase 15 §77/§137).
func (p *ShopifyProvider) ensureFrameComponent(ctx context.Context, req commerce.ProductUpsertRequest) (string, error) {
	title := frameComponentTitle(req.Product)
	input := map[string]any{
		"title":               title,
		"productType":         "MoonLight Frame",
		"status":              "DRAFT",
		"skipDefaultVariants": true,
		"metafields":          componentMetafields(req, "frame_component"),
		"productOptions": []map[string]any{
			{"name": "Frame Style", "values": frameStyleValues(req.Product)},
			{"name": "Frame Color", "values": frameColorValues(req.Product)},
		},
		"variants": frameComponentVariants(req.Product),
	}
	var out struct {
		ProductSet struct {
			Product struct {
				ID       string `json:"id"`
				Variants struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"variants"`
			} `json:"product"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"productSet"`
	}
	err := p.client.do(ctx, docFrameComponentSet, map[string]any{"input": input}, &out)
	if err != nil {
		return "", err
	}
	if len(out.ProductSet.UserErrors) > 0 {
		return "", capabilityError(out.ProductSet.UserErrors[0].Message)
	}
	if out.ProductSet.Product.ID == "" {
		return "", capabilityError("frame component identity missing")
	}
	return out.ProductSet.Product.ID, nil
}

// ensureBundleComponents attaches components to the sellable bundle
// product. Refusal of the bundle surface is the documented capability
// gate (§96) — never a silent native-variant fallback (§97).
func (p *ShopifyProvider) ensureBundleComponents(ctx context.Context, bundleProductID, baseComponentID, frameComponentID string, product commerce.CommerceProduct) error {
	input := map[string]any{
		"productId": bundleProductID,
		"components": []map[string]any{
			{"quantity": 1, "productId": baseComponentID},
			{"quantity": 1, "productId": frameComponentID,
				"optionSelections": []map[string]any{
					{"name": "Frame Style", "values": frameStyleValues(product)},
					{"name": "Frame Color", "values": frameColorValues(product)},
				}},
		},
	}
	var out struct {
		ProductBundleUpdate struct {
			ProductBundleOperation struct {
				OperationID string `json:"id"`
				Status      string `json:"status"`
			} `json:"productBundleOperation"`
			UserErrors []struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"userErrors"`
		} `json:"productBundleUpdate"`
	}
	if err := p.client.do(ctx, docBundleUpdate, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if len(out.ProductBundleUpdate.UserErrors) > 0 {
		return capabilityError(out.ProductBundleUpdate.UserErrors[0].Message)
	}
	operationID := out.ProductBundleUpdate.ProductBundleOperation.OperationID
	if operationID == "" {
		return capabilityError("bundle operation missing")
	}
	return p.awaitBundleOperation(ctx, operationID)
}

func (p *ShopifyProvider) awaitBundleOperation(ctx context.Context, operationID string) error {
	for attempt := 0; attempt < 6; attempt++ {
		var out struct {
			ProductOperation struct {
				Status     string `json:"status"`
				UserErrors []struct {
					Message string `json:"message"`
				} `json:"userErrors"`
			} `json:"productOperation"`
		}
		if err := p.client.do(ctx, docBundleOperation, map[string]any{"id": operationID}, &out); err != nil {
			return err
		}
		switch strings.ToUpper(out.ProductOperation.Status) {
		case "COMPLETE":
			return nil
		case "FAILED":
			if len(out.ProductOperation.UserErrors) > 0 {
				return capabilityError(out.ProductOperation.UserErrors[0].Message)
			}
			return capabilityError("bundle operation failed")
		}
	}
	return commerce.TemporaryError("bundle operation pending")
}

// bundleVariantIdentities resolves bundle-parent variant GIDs per
// configuration through the owner-authored option mapping (never label
// guessing): variant names follow the stable "Style / Color" convention
// written at creation.
func (p *ShopifyProvider) bundleVariantIdentities(ctx context.Context, req commerce.ProductUpsertRequest, bundleProductID string) (map[string]string, error) {
	var out struct {
		Product struct {
			Variants struct {
				Nodes []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"variants"`
		} `json:"product"`
	}
	query := `query MoonlightBundleVariants($id: ID!) {
  product(id: $id) { id variants(first: 100) { nodes { id name } } }
}`
	if err := p.client.do(ctx, query, map[string]any{"id": bundleProductID}, &out); err != nil {
		return nil, err
	}
	identities := make(map[string]string, len(req.Product.Configurations)+1)
	byName := make(map[string]string, len(out.Product.Variants.Nodes))
	for _, variant := range out.Product.Variants.Nodes {
		byName[variant.Name] = variant.ID
	}
	for _, configuration := range req.Product.Configurations {
		name := variantName(configuration)
		if id := byName[name]; id != "" {
			identities[configuration.ConfigurationID] = id
		}
	}
	if id := byName[noFrameVariantName]; id != "" {
		identities[commerce.NoFrameConfigurationID] = id
	}
	return identities, nil
}

const noFrameVariantName = "No Frame"

func variantName(configuration commerce.CommerceConfiguration) string {
	return displayLabel(configuration.StyleNameEN, configuration.StyleNameAR) + " / " +
		displayLabel(configuration.ColorNameEN, configuration.ColorNameAR)
}

func frameComponentTitle(product commerce.CommerceProduct) string {
	for _, name := range product.Names {
		if strings.EqualFold(name.Locale, "ar") && strings.TrimSpace(name.Name) != "" {
			return name.Name + " (إطار)"
		}
	}
	return product.SKU + " Frame"
}

func displayLabel(en *string, ar string) string {
	if en != nil && strings.TrimSpace(*en) != "" {
		return *en
	}
	return ar
}

func frameStyleValues(product commerce.CommerceProduct) []string {
	values := []string{noFrameVariantName}
	seen := map[string]bool{noFrameVariantName: true}
	for _, configuration := range product.Configurations {
		value := displayLabel(configuration.StyleNameEN, configuration.StyleNameAR)
		if !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func frameColorValues(product commerce.CommerceProduct) []string {
	values := []string{noFrameVariantName}
	seen := map[string]bool{noFrameVariantName: true}
	for _, configuration := range product.Configurations {
		value := displayLabel(configuration.ColorNameEN, configuration.ColorNameAR)
		if !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

// frameComponentVariants lists the EXPLICIT valid combinations (§13) as
// untracked component variants (§77: frames carry no stock).
func frameComponentVariants(product commerce.CommerceProduct) []map[string]any {
	variants := []map[string]any{
		{"optionValues": []map[string]any{
			{"optionName": "Frame Style", "name": noFrameVariantName},
			{"optionName": "Frame Color", "name": noFrameVariantName},
		}, "inventoryItem": map[string]any{"tracked": false}, "inventoryPolicy": "CONTINUE"},
	}
	for _, configuration := range product.Configurations {
		variants = append(variants, map[string]any{
			"optionValues": []map[string]any{
				{"optionName": "Frame Style", "name": displayLabel(configuration.StyleNameEN, configuration.StyleNameAR)},
				{"optionName": "Frame Color", "name": displayLabel(configuration.ColorNameEN, configuration.ColorNameAR)},
			},
			"inventoryItem":   map[string]any{"tracked": false},
			"inventoryPolicy": "CONTINUE",
			"price":           "0.00",
		})
	}
	return variants
}

// baseComponentID resolves the tracked base component: the mapped remote
// Product pre-dates the bundle (documented simple->framed transition,
// §116) and stays the inventory authority.
func (p *ShopifyProvider) baseComponentID(ctx context.Context, req commerce.ProductUpsertRequest, bundleProductID string) (string, error) {
	var out struct {
		Product struct {
			ID         string `json:"id"`
			Metafields struct {
				Nodes []struct {
					Namespace string `json:"namespace"`
					Key       string `json:"key"`
					Value     string `json:"value"`
				} `json:"nodes"`
			} `json:"metafields"`
		} `json:"product"`
	}
	query := `query MoonlightBundleBase($id: ID!) {
  product(id: $id) {
    id
    metafields(first: 20, namespace: "moonlight") { nodes { namespace key value } }
  }
}`
	if err := p.client.do(ctx, query, map[string]any{"id": bundleProductID}, &out); err != nil {
		return "", err
	}
	for _, field := range out.Product.Metafields.Nodes {
		if field.Key == "base_component_id" && field.Value != "" {
			return field.Value, nil
		}
	}
	// First framed sync: the current mapped Product IS the base
	// component. Record the role so later syncs and recovery can prove
	// ownership (§62/§100).
	if err := p.setOwnership(ctx, bundleProductID, []map[string]any{
		{"namespace": "moonlight", "key": "base_component_id", "value": bundleProductID, "type": "single_line_text_field"},
		{"namespace": "moonlight", "key": "role", "value": "base_component", "type": "single_line_text_field"},
	}); err != nil {
		return "", err
	}
	return bundleProductID, nil
}

// componentMetafields are inline productSet metafields (no ownerId —
// the productSet input owns them at creation) carrying the frozen
// MoonLight ownership pattern (§100).
func componentMetafields(req commerce.ProductUpsertRequest, role string) []map[string]any {
	return []map[string]any{
		{"namespace": metafieldNamespace, "key": "product_id", "value": req.ProductID, "type": "single_line_text_field"},
		{"namespace": metafieldNamespace, "key": "provider_key", "value": string(req.ProviderKey), "type": "single_line_text_field"},
		{"namespace": metafieldNamespace, "key": "role", "value": role, "type": "single_line_text_field"},
		{"namespace": metafieldNamespace, "key": "product_operation_key", "value": req.OperationKey, "type": "single_line_text_field"},
	}
}

// setBundleVariantPrices writes exact configured prices (base + delta,
// §38-§40/§215) on the bundle parent variants: the bundle parent
// variant's price is the customer-facing price per the official bundle
// semantics. Overflow and currency gaps are rejected, never rounded.
func (p *ShopifyProvider) setBundleVariantPrices(ctx context.Context, req commerce.ProductUpsertRequest, bundleProductID string, identities map[string]string) error {
	type variantPrice struct {
		ID    string `json:"id"`
		Price string `json:"price"`
	}
	var updates []variantPrice
	base, err := configuredPrice(req.Product.Prices, p.currency)
	if err != nil {
		return err
	}
	baseMinor, err := ParseMoneyString(base, p.currency)
	if err != nil {
		return err
	}
	if id := identities[commerce.NoFrameConfigurationID]; id != "" {
		updates = append(updates, variantPrice{ID: id, Price: base})
	}
	for _, configuration := range req.Product.Configurations {
		id := identities[configuration.ConfigurationID]
		if id == "" {
			continue
		}
		delta := configuration.PriceDeltaEGPMinor
		if strings.EqualFold(p.currency, "USD") {
			if configuration.PriceDeltaUSDMinor == nil {
				// §183: no coherent USD price — the choice is not
				// published in USD; its price stays base to avoid a
				// misleading amount while hidden.
				updates = append(updates, variantPrice{ID: id, Price: base})
				continue
			}
			delta = *configuration.PriceDeltaUSDMinor
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
		UserErrors []struct {
			Message string `json:"message"`
		} `json:"userErrors"`
	}
	variables := map[string]any{
		"productId": bundleProductID,
		"variants":  updates,
	}
	if err := p.client.do(ctx, docVariantPrices, variables, &out); err != nil {
		return err
	}
	if len(out.UserErrors) > 0 {
		return commerce.ValidationError(out.UserErrors[0].Message)
	}
	return nil
}
