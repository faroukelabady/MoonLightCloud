package shopify

// Phase 15-R1 F06-F10: Shopify frame-configuration bundle lifecycle.
//
// Representation (official semantics, shopify.dev/docs/apps/build/
// product-merchandising/bundles): the sellable Product is a BUNDLE whose
// components are (1) the tracked base papyrus Product — the ONLY
// inventory-tracked component, its managed variant carrying the
// canonical OnlineAvailable — and (2) an untracked "frame" component
// whose variants are the explicit valid combinations plus NO-FRAME.
// Bundle inventory derives from component inventory, so every customer
// choice consumes ONE base papyrus pool.
//
// Identity (F08): configuration identity is the bundle-parent variant
// GID recorded in durable configuration mappings AND in variant-level
// moonlight.configuration_id metadata. Display labels are never
// identity: equal or renamed labels keep distinct identities.
//
// Lifecycle (F07): the base, frame and bundle roles are DISTINCT remote
// resources with ownership metadata; discovery/adoption is
// ownership-proven before any create (a lost response can never produce
// a duplicate); the sellable identity becomes the bundle parent only
// after the representation and mappings are coherent.
//
// Capability (F06): deterministic schema/capability refusals return the
// stable SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE blocked state —
// never an unsafe native-variant fallback (§97), never a partially
// published representation. Network/unknown-side-effect behavior keeps
// the frozen F2 barrier semantics.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// CapabilityCode is the stable blocked-state code returned when the
// configured shop cannot represent frame options safely (§96).
const CapabilityCode = "SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE"

const (
	roleBundleParent       = "bundle_parent"
	roleBaseComponent      = "base_component"
	roleFrameComponent     = "frame_component"
	keyBaseComponentID     = "base_component_id"
	keyFrameComponentID    = "frame_component_id"
	keyConfigurationIDMeta = "configuration_id"
	keyRole                = "role"
	noFrameVariantName     = "No Frame"
)

// metafieldValue reads one owned metafield value from a remote product.
func metafieldValue(nodes []gqlMetafield, key string) string {
	for _, field := range nodes {
		if field.Namespace == metafieldNamespace && field.Key == key {
			return field.Value
		}
	}
	return ""
}

// bundle documents (2026-10 API; shapes verified against the official
// ProductSetInput / ProductBundleComponentInput / ProductVariant docs).
const (
	docBundleCreate = `mutation MoonlightBundleCreate($input: ProductBundleCreateInput!) {
  productBundleCreate(input: $input) {
    productBundleOperation { id status }
    userErrors { message field code }
  }
}`
	docBundleUpdate = `mutation MoonlightBundleUpdate($input: ProductBundleUpdateInput!) {
  productBundleUpdate(input: $input) {
    productBundleOperation { id status }
    userErrors { message field code }
  }
}`
	docBundleOperation = `query MoonlightBundleOperation($id: ID!) {
  productOperation(id: $id) {
    ... on ProductBundleOperation { id status product { id } userErrors { field message code } }
  }
}`
	docBundleState = `query MoonlightBundleState($id: ID!) {
  product(id: $id) {
    id
    metafields(first: 20, namespace: "moonlight") { nodes { namespace key value } }
    variants(first: 100) {
      nodes {
        id title
        metafields(first: 10, namespace: "moonlight") { nodes { namespace key value } }
      }
    }
  }
}`
	docFrameComponentSet = `mutation MoonlightFrameComponentSet($input: ProductSetInput!) {
  productSet(synchronous: true, input: $input) {
    product {
      id
      options { id name }
      variants(first: 100) { nodes { id title } }
    }
    userErrors { field message }
  }
}`
	docVariantPrices = `mutation MoonlightVariantPrices($productId: ID!, $variants: [ProductVariantsBulkInput!]!) {
  productVariantsBulkUpdate(productId: $productId, variants: $variants) {
    productVariants { id }
    userErrors { field message }
  }
}`
	docBundleDiscover = `query MoonlightBundleDiscover($query: String!) {
  products(first: 10, query: $query) {
    nodes { id }
  }
}`
)

// capabilityError is the stable permanent provider error for shops that
// cannot represent frame options safely (§96/§216): bounded, no retry
// storm, no unsafe fallback.
func capabilityError(detail string) error {
	return commerce.ValidationError(CapabilityCode + ": " + detail)
}

// classifyBundleFailure maps DETERMINISTIC pre-execution refusals
// (validation-class provider errors: schema/capability rejections) to the
// stable capability code. Network, throttle and unknown-side-effect
// failures keep the frozen retry/F2 classification untouched (F06:
// never classify every GraphQL error identically).
func classifyBundleFailure(err error) error {
	if err == nil {
		return nil
	}
	var providerErr *commerce.ProviderError
	if errors.As(err, &providerErr) && providerErr.Kind == commerce.ErrorValidation {
		return capabilityError(providerErr.Message)
	}
	return err
}

// bundleState is the proven-ownership view of one remote bundle parent.
type bundleState struct {
	bundleProductID  string
	baseComponentID  string
	frameComponentID string
	variantIDs       map[string]string // configuration id -> bundle variant GID
}

// syncShopifyConfigurations converges the bundle representation and
// returns the sellable identity (bundle parent) plus configuration
// identities (configuration GID map, including the NO-FRAME sentinel).
func (p *ShopifyProvider) syncShopifyConfigurations(ctx context.Context, req commerce.ProductUpsertRequest, baseProductID string) (string, map[string]string, error) {
	// Currency-coherent choices only (F09): a configuration without a
	// delta for the adapter currency is NOT a customer choice.
	choices := chooseConfigurations(req.Product, p.currency)
	state, err := p.discoverBundle(ctx, req, baseProductID)
	if err != nil {
		return "", nil, err
	}
	if state == nil {
		frameComponentID, frameOptions, err := p.ensureFrameComponent(ctx, req, baseProductID, choices)
		if err != nil {
			return "", nil, err
		}
		bundleID, err := p.createBundle(ctx, req, baseProductID, frameComponentID, frameOptions, choices)
		if err != nil {
			return "", nil, err
		}
		state = &bundleState{bundleProductID: bundleID, baseComponentID: baseProductID, frameComponentID: frameComponentID}
	}
	// Converge component selection + prices on the discovered/created
	// bundle; identities come from variant configuration metadata (F08).
	if err := p.convergeBundleComponents(ctx, state, req, choices); err != nil {
		return "", nil, err
	}
	identities, err := p.bundleVariantIdentities(ctx, state.bundleProductID, req.Product)
	if err != nil {
		return "", nil, err
	}
	if err := p.setBundleVariantPrices(ctx, state.bundleProductID, req.Product, identities, choices); err != nil {
		return "", nil, err
	}
	return state.bundleProductID, identities, nil
}

// discoverBundle finds the existing bundle parent through ownership
// metadata on the base component (F07): recovery after lost responses
// re-adopts the SAME resources; nothing is ever created blindly.
func (p *ShopifyProvider) discoverBundle(ctx context.Context, req commerce.ProductUpsertRequest, baseProductID string) (*bundleState, error) {
	base, err := p.loadProduct(ctx, baseProductID)
	if err != nil {
		return nil, err
	}
	state := &bundleState{baseComponentID: baseProductID}
	state.bundleProductID = metafieldValue(base.Metafields.Nodes, keyBundleParentID)
	state.frameComponentID = metafieldValue(base.Metafields.Nodes, keyFrameComponentID)
	if state.bundleProductID == "" {
		// Fall back to ownership discovery by metadata (lost-response
		// recovery when even the base metafield write was lost).
		found, err := p.discoverByRole(ctx, roleBundleParent, req.ProductID)
		if err != nil {
			return nil, err
		}
		state.bundleProductID = found
	}
	if state.bundleProductID == "" {
		return nil, nil
	}
	bundle, err := p.loadProduct(ctx, state.bundleProductID)
	if err != nil {
		return nil, err
	}
	// Ownership proof before adoption (F07/F08): wrong owner = never adopted.
	if metafieldValue(bundle.Metafields.Nodes, metafieldProductID) != req.ProductID ||
		metafieldValue(bundle.Metafields.Nodes, metafieldProviderKey) != string(p.key) ||
		metafieldValue(bundle.Metafields.Nodes, keyRole) != roleBundleParent {
		return nil, capabilityError("bundle ownership mismatch")
	}
	return state, nil
}

// discoverByRole locates one of OUR role-stamped resources by stable
// metadata (never by title/labels, F08).
func (p *ShopifyProvider) discoverByRole(ctx context.Context, role, productID string) (string, error) {
	query := fmt.Sprintf("metafields.%s.%s:%s AND metafields.%s.%s:%s",
		metafieldNamespace, keyRole, role,
		metafieldNamespace, metafieldProductID, productID)
	var out struct {
		Products struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"products"`
	}
	if err := p.client.do(ctx, docBundleDiscover, map[string]any{"query": query}, &out); err != nil {
		return "", err
	}
	switch len(out.Products.Nodes) {
	case 0:
		return "", nil
	case 1:
		return out.Products.Nodes[0].ID, nil
	default:
		// Ambiguous duplicate owners: bounded safe failure (F07), never a
		// guess between conflicting resources.
		return "", capabilityError("ambiguous owned resources")
	}
}

const keyBundleParentID = "bundle_parent_id"

// ensureFrameComponent creates OR adopts the untracked frame component
// (F07 recovery). Ownership metadata proves the tuple; a lost create
// response re-adopts the same component.
func (p *ShopifyProvider) ensureFrameComponent(ctx context.Context, req commerce.ProductUpsertRequest, baseProductID string, choices []commerce.CommerceConfiguration) (string, []map[string]any, error) {
	if existing, err := p.discoverByRole(ctx, roleFrameComponent, req.ProductID); err != nil {
		return "", nil, err
	} else if existing != "" {
		state, err := p.loadProduct(ctx, existing)
		if err != nil {
			return "", nil, err
		}
		if metafieldValue(state.Metafields.Nodes, metafieldProductID) != req.ProductID || metafieldValue(state.Metafields.Nodes, metafieldProviderKey) != string(p.key) {
			return "", nil, capabilityError("frame component ownership mismatch")
		}
		return existing, frameComponentOptions(choices), nil
	}
	options := frameComponentOptions(choices)
	input := map[string]any{
		"title":       frameComponentTitle(req.Product),
		"productType": "MoonLight Frame",
		"status":      "DRAFT",
		"metafields":  componentMetafields(req, roleFrameComponent),
		"productOptions": []map[string]any{
			{"name": "Frame Style", "values": frameStyleValues(choices)},
			{"name": "Frame Color", "values": frameColorValues(choices)},
		},
		"variants": frameComponentVariants(choices),
	}
	var out struct {
		ProductSet struct {
			Product struct {
				ID      string `json:"id"`
				Options []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"options"`
			} `json:"product"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"productSet"`
	}
	if err := p.client.do(ctx, docFrameComponentSet, map[string]any{"input": input}, &out); err != nil {
		return "", nil, classifyBundleFailure(err)
	}
	if len(out.ProductSet.UserErrors) > 0 {
		return "", nil, capabilityError(out.ProductSet.UserErrors[0].Message)
	}
	if out.ProductSet.Product.ID == "" {
		// Ambiguous create (response unusable): recover by ownership
		// discovery instead of issuing another blind create (F07).
		found, err := p.discoverByRole(ctx, roleFrameComponent, req.ProductID)
		if err != nil {
			return "", nil, err
		}
		if found == "" {
			return "", nil, capabilityError("frame component identity missing")
		}
		out.ProductSet.Product.ID = found
	}
	// Record role ownership on the BASE component for restart-safe
	// recovery (with ownerId — standalone metafieldsSet requires it, F06).
	if err := p.setOwnership(ctx, baseProductID, []map[string]any{
		{"ownerId": baseProductID, "namespace": metafieldNamespace, "key": keyFrameComponentID, "type": "single_line_text_field", "value": out.ProductSet.Product.ID},
	}); err != nil {
		return "", nil, err
	}
	options = frameComponentOptions(choices)
	_ = out.ProductSet.Product.Options
	return out.ProductSet.Product.ID, options, nil
}

// createBundle creates the distinct sellable bundle parent through the
// official bundle mutation and adopts the OPERATION's returned Product
// identity (F07 — never the base product, never self-component).
func (p *ShopifyProvider) createBundle(ctx context.Context, req commerce.ProductUpsertRequest, baseProductID, frameComponentID string, options []map[string]any, choices []commerce.CommerceConfiguration) (string, error) {
	bundleID, err := p.discoverByRole(ctx, roleBundleParent, req.ProductID)
	if err != nil {
		return "", err
	}
	if bundleID != "" {
		return bundleID, nil
	}
	input := map[string]any{
		"title": bundleTitle(req.Product),
		"components": []map[string]any{
			{"quantity": 1, "productId": baseProductID},
			{"quantity": 1, "productId": frameComponentID, "optionSelections": options},
		},
	}
	var out struct {
		ProductBundleOperation struct {
			OperationID string `json:"id"`
		} `json:"productBundleOperation"`
		UserErrors []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"userErrors"`
	}
	err = classifyBundleFailure(p.client.do(ctx, docBundleCreate, map[string]any{"input": input}, &out))
	if err != nil {
		// Ambiguous create: the bundle may already exist (response lost).
		if found, findErr := p.discoverByRole(ctx, roleBundleParent, req.ProductID); findErr == nil && found != "" {
			return found, nil
		}
		return "", err
	}
	if len(out.UserErrors) > 0 {
		return "", capabilityError(out.UserErrors[0].Message)
	}
	operationID := out.ProductBundleOperation.OperationID
	if operationID == "" {
		return "", capabilityError("bundle operation missing")
	}
	productID, err := p.awaitBundleOperation(ctx, operationID)
	if err != nil {
		// Operation may have completed despite the timeout: adopt rather
		// than duplicate (F07).
		if found, findErr := p.discoverByRole(ctx, roleBundleParent, req.ProductID); findErr == nil && found != "" {
			return found, nil
		}
		return "", err
	}
	if productID == "" || productID == baseProductID {
		return "", capabilityError("bundle identity invalid")
	}
	// Role ownership stamp (ownerId required for standalone metafieldsSet).
	if err := p.setOwnership(ctx, productID, []map[string]any{
		{"ownerId": productID, "namespace": metafieldNamespace, "key": metafieldProductID, "type": "single_line_text_field", "value": req.ProductID},
		{"ownerId": productID, "namespace": metafieldNamespace, "key": metafieldProviderKey, "type": "single_line_text_field", "value": string(p.key)},
		{"ownerId": productID, "namespace": metafieldNamespace, "key": keyRole, "type": "single_line_text_field", "value": roleBundleParent},
		{"ownerId": productID, "namespace": metafieldNamespace, "key": keyBaseComponentID, "type": "single_line_text_field", "value": baseProductID},
	}); err != nil {
		return "", err
	}
	if err := p.setOwnership(ctx, baseProductID, []map[string]any{
		{"ownerId": baseProductID, "namespace": metafieldNamespace, "key": keyBundleParentID, "type": "single_line_text_field", "value": productID},
	}); err != nil {
		return "", err
	}
	return productID, nil
}

// awaitBundleOperation adopts the asynchronous operation's returned
// Product identity (F06/F07): never discarded, never re-created.
func (p *ShopifyProvider) awaitBundleOperation(ctx context.Context, operationID string) (string, error) {
	var out struct {
		ProductOperation struct {
			Status  string `json:"status"`
			Product *struct {
				ID string `json:"id"`
			} `json:"product"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"productOperation"`
	}
	for attempt := 0; attempt < 6; attempt++ {
		if err := p.client.do(ctx, docBundleOperation, map[string]any{"id": operationID}, &out); err != nil {
			return "", classifyBundleFailure(err)
		}
		switch strings.ToUpper(out.ProductOperation.Status) {
		case "COMPLETE":
			if out.ProductOperation.Product == nil {
				return "", capabilityError("bundle operation product missing")
			}
			return out.ProductOperation.Product.ID, nil
		case "FAILED":
			if len(out.ProductOperation.UserErrors) > 0 {
				return "", capabilityError(out.ProductOperation.UserErrors[0].Message)
			}
			return "", capabilityError("bundle operation failed")
		}
	}
	return "", commerce.TemporaryError("bundle operation pending")
}

// convergeBundleComponents attaches base + frame components to the
// sellable bundle with the enabled, currency-coherent choices only (F09).
func (p *ShopifyProvider) convergeBundleComponents(ctx context.Context, state *bundleState, req commerce.ProductUpsertRequest, choices []commerce.CommerceConfiguration) error {
	if state.frameComponentID == "" {
		frameID, options, err := p.ensureFrameComponent(ctx, req, state.baseComponentID, choices)
		if err != nil {
			return err
		}
		state.frameComponentID = frameID
		return p.bundleUpdate(ctx, state.bundleProductID, state.baseComponentID, frameID, options)
	}
	return p.bundleUpdate(ctx, state.bundleProductID, state.baseComponentID, state.frameComponentID, frameComponentOptions(choices))
}

func (p *ShopifyProvider) bundleUpdate(ctx context.Context, bundleID, baseComponentID, frameComponentID string, options []map[string]any) error {
	input := map[string]any{
		"productId": bundleID,
		"components": []map[string]any{
			{"quantity": 1, "productId": baseComponentID},
			{"quantity": 1, "productId": frameComponentID, "optionSelections": options},
		},
	}
	var out struct {
		ProductBundleUpdate struct {
			ProductBundleOperation struct {
				OperationID string `json:"id"`
			} `json:"productBundleOperation"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"productBundleUpdate"`
	}
	if err := p.client.do(ctx, docBundleUpdate, map[string]any{"input": input}, &out); err != nil {
		return classifyBundleFailure(err)
	}
	if len(out.ProductBundleUpdate.UserErrors) > 0 {
		return capabilityError(out.ProductBundleUpdate.UserErrors[0].Message)
	}
	operationID := out.ProductBundleUpdate.ProductBundleOperation.OperationID
	if operationID == "" {
		return capabilityError("bundle operation missing")
	}
	_, err := p.awaitBundleOperation(ctx, operationID)
	return err
}

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
	base, err := configuredPrice(product.Prices, p.currency)
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
		return commerce.ValidationError("bundle price refusal: " + out.ProductVariantsBulkUpdate.UserErrors[0].Message)
	}
	return nil
}

// frameComponentOptions are the option selections advertised to the
// bundle for the enabled, currency-coherent choices (explicit valid
// combinations only — never a Cartesian product).
func frameComponentOptions(choices []commerce.CommerceConfiguration) []map[string]any {
	return []map[string]any{
		{"name": "Frame Style", "values": frameStyleValues(choices)},
		{"name": "Frame Color", "values": frameColorValues(choices)},
	}
}

// frameComponentVariants lists EXPLICIT valid combinations (§13) as
// untracked variants (§77: frames carry no stock). Disabled and
// currency-incomplete choices are never created (F09).
func frameComponentVariants(choices []commerce.CommerceConfiguration) []map[string]any {
	variants := []map[string]any{
		{"optionValues": []map[string]any{
			{"optionName": "Frame Style", "name": noFrameVariantName},
			{"optionName": "Frame Color", "name": noFrameVariantName},
		}, "inventoryItem": map[string]any{"tracked": false}, "inventoryPolicy": "CONTINUE"},
	}
	for _, configuration := range choices {
		variants = append(variants, map[string]any{
			"optionValues": []map[string]any{
				{"optionName": "Frame Style", "name": displayLabel(configuration.StyleNameEN, configuration.StyleNameAR)},
				{"optionName": "Frame Color", "name": displayLabel(configuration.ColorNameEN, configuration.ColorNameAR)},
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

func variantNameFromTitle(title string) string { return title }

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

func frameStyleValues(choices []commerce.CommerceConfiguration) []string {
	values := []string{noFrameVariantName}
	seen := map[string]bool{noFrameVariantName: true}
	for _, configuration := range choices {
		value := displayLabel(configuration.StyleNameEN, configuration.StyleNameAR)
		if !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func frameColorValues(choices []commerce.CommerceConfiguration) []string {
	values := []string{noFrameVariantName}
	seen := map[string]bool{noFrameVariantName: true}
	for _, configuration := range choices {
		value := displayLabel(configuration.ColorNameEN, configuration.ColorNameAR)
		if !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
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
