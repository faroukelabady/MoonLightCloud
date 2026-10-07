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
	"crypto/sha256"
	"encoding/json"
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

// productGID converts a persisted decimal external identity to the
// GraphQL GID form used at the transport boundary (F07); GIDs pass
// through unchanged. Persisted identity format is never rewritten.
func productGID(externalOrGID string) (string, error) {
	if strings.HasPrefix(externalOrGID, "gid://") {
		return externalOrGID, nil
	}
	externalID, err := CanonicalDecimalID(externalOrGID)
	if err != nil {
		return "", commerce.ValidationError("shopify product identity is malformed")
	}
	return FormatGID(ResourceProduct, externalID), nil
}

// canonicalFromGID reduces a product GID to the persisted decimal form.
func canonicalFromGID(gid string) string {
	externalID, err := ParseGID(gid, ResourceProduct)
	if err != nil {
		return gid
	}
	return externalID
}

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
// cannot represent frame options safely (§96/§216). Detail passes the
// FROZEN Shopify scrub/bound contract (F19): remote text is scrubbed of
// credentials and bounded exactly like every other provider error —
// never concatenated raw into caller-visible messages.
func (p *ShopifyProvider) capabilityError(detail string) error {
	return commerce.ValidationError(p.client.safeMessage(CapabilityCode + ": " + detail))
}

// classifyBundleFailure maps DETERMINISTIC pre-execution refusals
// (validation-class provider errors: schema/capability rejections) to the
// stable capability code. Network, throttle and unknown-side-effect
// failures keep the frozen retry/F2 classification untouched (F06:
// never classify every GraphQL error identically).
func (p *ShopifyProvider) classifyBundleFailure(err error) error {
	if err == nil {
		return nil
	}
	var providerErr *commerce.ProviderError
	if errors.As(err, &providerErr) && providerErr.Kind == commerce.ErrorValidation {
		return p.capabilityError(p.client.safeMessage(providerErr.Message))
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
	choices := chooseConfigurations(req.Product, p.currency)
	state, err := p.discoverBundle(ctx, req, baseProductID)
	if err != nil {
		return "", nil, err
	}
	if state != nil {
		remote, err := p.loadProduct(ctx, state.bundleProductID)
		if err != nil {
			return "", nil, err
		}
		if err := validateStampedVariants(remote); err != nil {
			return "", nil, err
		}
	}
	// F09: the frame component's explicit combination set converges on
	// EVERY pass (create or existing) BEFORE bundle convergence is
	// claimed — disabled/missing-currency choices stop being represented.
	frameID, optionIDs, err := p.ensureFrameComponent(ctx, req, baseProductID, choices)
	if err != nil {
		return "", nil, err
	}
	if state == nil {
		bundleID, err := p.createBundle(ctx, req, baseProductID, frameID, frameComponentOptions(optionIDs, choices), choices)
		if err != nil {
			return "", nil, err
		}
		state = &bundleState{bundleProductID: bundleID, baseComponentID: baseProductID, frameComponentID: frameID}
	} else {
		state.frameComponentID = frameID
		if err := p.bundleUpdate(ctx, state.bundleProductID, state.baseComponentID, frameID, frameComponentOptions(optionIDs, choices)); err != nil {
			return "", nil, err
		}
	}
	// F08: establish/confirm ownership-proven configuration identity on
	// the sellable bundle variants (metadata-stamped; establishment tuples
	// are collision-disambiguated so they can never merge identities),
	// then apply exact prices. An empty/partial map with intended choices
	// is a bounded failure — never synchronized success.
	identities, err := p.establishBundleVariantIdentity(ctx, state.bundleProductID, req, choices)
	if err != nil {
		return "", nil, err
	}
	if err := p.setBundleVariantPrices(ctx, state.bundleProductID, req.Product, identities, choices); err != nil {
		return "", nil, err
	}
	// F20: the sellable bundle parent is the customer-facing Product —
	// its canonical content and required activation converge on every
	// pass, not only at creation.
	if err := p.convergeBundleParent(ctx, req, state.bundleProductID); err != nil {
		return "", nil, err
	}
	return state.bundleProductID, identities, nil
}

// convergeBundleParent converges the CANONICAL owned sellable content of
// the bundle parent (F20): the same provider content set the hidden base
// receives — title and description — through targeted writes only, so
// unrelated merchant fields (tags, collections, media, SEO, vendor,
// product type) survive. Required activation is written only when
// publication is requested and the parent is not ACTIVE: publishing an
// explicitly DRAFT parent must converge its status for channel
// visibility. Unpublication never drafts the parent.
func (p *ShopifyProvider) convergeBundleParent(ctx context.Context, req commerce.ProductUpsertRequest, bundleGID string) error {
	state, err := p.loadProduct(ctx, bundleGID)
	if err != nil {
		return err
	}
	if state == nil {
		return p.capabilityError("bundle parent missing")
	}
	input := map[string]any{
		"id":              bundleGID,
		"title":           bundleTitle(req.Product),
		"descriptionHtml": localizedDescription(req.Product.Descriptions),
	}
	if req.Published && !strings.EqualFold(state.Status, "ACTIVE") {
		// Shopify requires ACTIVE Product status for channel visibility;
		// publication membership alone is not enough.
		input["status"] = "ACTIVE"
	}
	return p.productUpdate(ctx, input)
}

// discoverBundle finds the existing bundle parent through ownership
// metadata on the base component (F07): recovery after lost responses
// re-adopts the SAME resources; nothing is ever created blindly.
func (p *ShopifyProvider) discoverBundle(ctx context.Context, req commerce.ProductUpsertRequest, baseProductID string) (*bundleState, error) {
	baseGID, err := productGID(baseProductID)
	if err != nil {
		return nil, err
	}
	baseProductID = baseGID
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
		return nil, p.capabilityError("bundle ownership mismatch")
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
		return "", p.capabilityError("ambiguous owned resources")
	}
}

const keyBundleParentID = "bundle_parent_id"

// ensureFrameComponent creates OR adopts the untracked frame component
// (F07 recovery). Ownership metadata proves the tuple; a lost create
// response re-adopts the same component.
func (p *ShopifyProvider) ensureFrameComponent(ctx context.Context, req commerce.ProductUpsertRequest, baseProductID string, choices []commerce.CommerceConfiguration) (string, map[string]string, error) {
	styleValues := frameStyleValues(choices)
	colorValues := frameColorValues(choices)
	variantInputs := frameComponentVariants(choices)
	optionsInput := []map[string]any{
		{"name": "Frame Style", "values": optionValueList(styleValues)},
		{"name": "Frame Color", "values": optionValueList(colorValues)},
	}
	// Ownership-first discovery (F07 recovery): a lost create response
	// re-adopts the same component; nothing is created blindly.
	existing, err := p.discoverByRole(ctx, roleFrameComponent, req.ProductID)
	if err != nil {
		return "", nil, err
	}
	if existing != "" {
		state, err := p.loadProduct(ctx, existing)
		if err != nil {
			return "", nil, err
		}
		if state == nil || metafieldValue(state.Metafields.Nodes, keyRole) != roleFrameComponent || metafieldValue(state.Metafields.Nodes, metafieldProductID) != req.ProductID ||
			metafieldValue(state.Metafields.Nodes, metafieldProviderKey) != string(p.key) {
			return "", nil, p.capabilityError(p.client.safeMessage("frame component ownership mismatch"))
		}
		// Preserve provider variant identities across label and ordering edits.
		existingIDs := map[string]string{}
		for _, v := range state.Variants.Nodes {
			id := metafieldValue(v.Metafields.Nodes, keyConfigurationIDMeta)
			if id == "" || existingIDs[id] != "" {
				return "", nil, p.capabilityError("ambiguous frame configuration identity")
			}
			existingIDs[id] = v.ID
		}
		for _, input := range variantInputs {
			fields := input["metafields"].([]map[string]any)
			id := fields[0]["value"].(string)
			if existingID := existingIDs[id]; existingID != "" {
				input["id"] = existingID
			}
		}
		// F09: converge the EXISTING component's explicit combination set.
		input := map[string]any{
			"id":             existing,
			"productOptions": optionsInput,
			"variants":       variantInputs,
		}
		var out struct {
			ProductSet struct {
				Product struct {
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
		if err := p.classifyBundleFailure(p.client.do(ctx, docFrameComponentSet, map[string]any{"input": input}, &out)); err != nil {
			return "", nil, err
		}
		if len(out.ProductSet.UserErrors) > 0 {
			return "", nil, p.capabilityError(p.client.safeMessage(out.ProductSet.UserErrors[0].Message))
		}
		return existing, optionIDsFrom(out.ProductSet.Product.Options), nil
	}
	input := map[string]any{
		"title":          frameComponentTitle(req.Product),
		"productType":    "MoonLight Frame",
		"status":         "DRAFT",
		"metafields":     componentMetafields(req, roleFrameComponent),
		"productOptions": optionsInput,
		"variants":       variantInputs,
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
	if err := p.classifyBundleFailure(p.client.do(ctx, docFrameComponentSet, map[string]any{"input": input}, &out)); err != nil {
		return "", nil, err
	}
	if len(out.ProductSet.UserErrors) > 0 {
		return "", nil, p.capabilityError(p.client.safeMessage(out.ProductSet.UserErrors[0].Message))
	}
	if out.ProductSet.Product.ID == "" {
		found, err := p.discoverByRole(ctx, roleFrameComponent, req.ProductID)
		if err != nil {
			return "", nil, err
		}
		if found == "" {
			return "", nil, p.capabilityError(p.client.safeMessage("frame component identity missing"))
		}
		out.ProductSet.Product.ID = found
	}
	if err := p.setOwnership(ctx, baseProductID, []map[string]any{
		{"ownerId": baseProductID, "namespace": metafieldNamespace, "key": keyFrameComponentID, "type": "single_line_text_field", "value": out.ProductSet.Product.ID},
	}); err != nil {
		return "", nil, err
	}
	return out.ProductSet.Product.ID, optionIDsFrom(out.ProductSet.Product.Options), nil
}

// optionIDsFrom maps option names to their provider option identifiers
// (F06: optionSelections require componentOptionId).
func optionIDsFrom(options []struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}) map[string]string {
	ids := make(map[string]string, len(options))
	for _, option := range options {
		ids[option.Name] = option.ID
	}
	return ids
}

// createBundle creates the distinct sellable bundle parent through the
// official bundle mutation and adopts the OPERATION's returned Product
// identity (F07 — never the base product, never self-component).
func (p *ShopifyProvider) createBundle(ctx context.Context, req commerce.ProductUpsertRequest, baseProductID, frameComponentID string, options []map[string]any, choices []commerce.CommerceConfiguration) (string, error) {
	store, err := commerce.ProductAsyncReceipts(ctx)
	if err != nil {
		return "", err
	}
	receipt, hasReceipt, err := store.LoadAsync(ctx, "bundle_create")
	if err != nil {
		return "", err
	}
	// buildCreate renders the exact physical create request; its digest
	// is the durable semantic intent recorded on the receipt at
	// acknowledgement (identical bytes hash identically).
	buildCreate := func() (map[string]any, string, error) {
		baseOptions, err := p.baseComponentOptions(ctx, baseProductID, req.Product.SKU)
		if err != nil {
			return nil, "", err
		}
		input := map[string]any{
			"title": bundleTitle(req.Product),
			"components": []map[string]any{
				{"quantity": 1, "productId": baseProductID, "optionSelections": baseOptions},
				{"quantity": 1, "productId": frameComponentID, "optionSelections": options},
			},
		}
		payload, _ := json.Marshal(gqlRequest{Query: docBundleCreate, Variables: map[string]any{"input": input}})
		return input, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
	}
	if hasReceipt {
		switch receipt.State {
		case "failed":
			// Errors do not prove absence of a Product. Known execution
			// evidence fences every fresh create, regardless of intent.
			if receipt.ProductID != "" {
				return "", p.capabilityError("bundle operation failed with product; reconciliation required")
			}
			_, intent, err := buildCreate()
			if err != nil {
				return "", err
			}
			if receipt.Intent == intent {
				return "", p.capabilityError("bundle operation failed")
			}
			// Older failed receipts discarded Product IDs. Re-read the
			// original operation before authorizing distinct corrected
			// work, and enrich that receipt if it returned a Product.
			if err := p.verifyFailedBundleCreate(ctx, receipt); err != nil {
				return "", err
			}
		case "pending":
			productID, err := p.awaitBundleOperation(ctx, receipt.OperationID)
			if err != nil {
				return "", err
			}
			return p.adoptCompletedBundle(ctx, req, productID, baseProductID, frameComponentID)
		default:
			return p.adoptCompletedBundle(ctx, req, receipt.ProductID, baseProductID, frameComponentID)
		}
	}
	bundleID, err := p.discoverByRole(ctx, roleBundleParent, req.ProductID)
	if err != nil {
		return "", err
	}
	if bundleID != "" {
		return bundleID, nil
	}
	input, _, err := buildCreate()
	if err != nil {
		return "", err
	}
	var out struct {
		ProductBundleCreate struct {
			ProductBundleOperation struct {
				OperationID string `json:"id"`
			} `json:"productBundleOperation"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"productBundleCreate"`
	}
	err = p.classifyBundleFailure(p.client.do(ctx, docBundleCreate, map[string]any{"input": input}, &out))
	if err != nil {
		// Ambiguous create: the bundle may already exist (response lost).
		if found, findErr := p.discoverByRole(ctx, roleBundleParent, req.ProductID); findErr == nil && found != "" {
			return found, nil
		}
		return "", err
	}
	if len(out.ProductBundleCreate.UserErrors) > 0 {
		return "", p.capabilityError(p.client.safeMessage(out.ProductBundleCreate.UserErrors[0].Message))
	}
	operationID := out.ProductBundleCreate.ProductBundleOperation.OperationID
	if operationID == "" {
		return "", p.capabilityError("bundle operation missing")
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
	return p.adoptCompletedBundle(ctx, req, productID, baseProductID, frameComponentID)
}

func (p *ShopifyProvider) adoptCompletedBundle(ctx context.Context, req commerce.ProductUpsertRequest, productID, baseProductID, frameComponentID string) (string, error) {
	if productID == "" || productID == baseProductID {
		return "", p.capabilityError("bundle identity invalid")
	}
	// Role ownership stamp (ownerId required for standalone metafieldsSet).
	if err := p.setOwnership(ctx, productID, []map[string]any{
		{"ownerId": productID, "namespace": metafieldNamespace, "key": metafieldProductID, "type": "single_line_text_field", "value": req.ProductID},
		{"ownerId": productID, "namespace": metafieldNamespace, "key": metafieldProviderKey, "type": "single_line_text_field", "value": string(p.key)},
		{"ownerId": productID, "namespace": metafieldNamespace, "key": keyRole, "type": "single_line_text_field", "value": roleBundleParent},
		{"ownerId": productID, "namespace": metafieldNamespace, "key": keyBaseComponentID, "type": "single_line_text_field", "value": baseProductID},
		{"ownerId": productID, "namespace": metafieldNamespace, "key": keyFrameComponentID, "type": "single_line_text_field", "value": frameComponentID},
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

type bundleOperationState struct {
	Status     string          `json:"status"`
	Product    json.RawMessage `json:"product"`
	UserErrors []struct {
		Message string `json:"message"`
	} `json:"userErrors"`
}

// An explicit null is no-Product evidence. An absent field or malformed
// identity is not; neither may authorize replacement creation.
func (state bundleOperationState) productID() (string, error) {
	if len(state.Product) == 0 {
		return "", commerce.TemporaryError("bundle operation product evidence missing")
	}
	if strings.TrimSpace(string(state.Product)) == "null" {
		return "", nil
	}
	var product struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(state.Product, &product); err != nil {
		return "", commerce.TemporaryError("bundle operation product invalid")
	}
	if _, err := ParseGID(product.ID, ResourceProduct); err != nil {
		return "", commerce.TemporaryError("bundle operation product invalid")
	}
	return product.ID, nil
}

func (p *ShopifyProvider) readBundleOperation(ctx context.Context, operationID string) (bundleOperationState, error) {
	var out struct {
		ProductOperation bundleOperationState `json:"productOperation"`
	}
	if err := p.client.do(ctx, docBundleOperation, map[string]any{"id": operationID}, &out); err != nil {
		return bundleOperationState{}, p.classifyBundleFailure(err)
	}
	return out.ProductOperation, nil
}

func (p *ShopifyProvider) verifyFailedBundleCreate(ctx context.Context, receipt commerce.AsyncProductReceipt) error {
	state, err := p.readBundleOperation(ctx, receipt.OperationID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(state.Status, "COMPLETE") {
		return commerce.TemporaryError("bundle failure evidence incomplete")
	}
	productID, err := state.productID()
	if err != nil {
		return err
	}
	if productID != "" {
		store, err := commerce.ProductAsyncReceipts(ctx)
		if err != nil {
			return err
		}
		if err := store.FinishAsync(ctx, receipt.OperationID, "failed", productID); err != nil {
			return err
		}
		return p.capabilityError("bundle operation failed with product; reconciliation required")
	}
	if len(state.UserErrors) == 0 {
		return commerce.TemporaryError("bundle failure evidence incomplete")
	}
	return nil
}

// awaitBundleOperation retains the returned Product even on failure;
// errors alone never establish that creation had no side effects.
func (p *ShopifyProvider) awaitBundleOperation(ctx context.Context, operationID string) (string, error) {
	for attempt := 0; attempt < 6; attempt++ {
		state, err := p.readBundleOperation(ctx, operationID)
		if err != nil {
			return "", err
		}
		switch strings.ToUpper(state.Status) {
		case "COMPLETE":
			store, err := commerce.ProductAsyncReceipts(ctx)
			if err != nil {
				return "", err
			}
			productID, err := state.productID()
			if err != nil {
				return "", err
			}
			if len(state.UserErrors) > 0 {
				if err := store.FinishAsync(ctx, operationID, "failed", productID); err != nil {
					return "", err
				}
				return "", p.capabilityError(state.UserErrors[0].Message)
			}
			if productID == "" {
				return "", p.capabilityError("bundle operation product missing")
			}
			if err := store.FinishAsync(ctx, operationID, "completed", productID); err != nil {
				return "", err
			}
			return productID, nil
		case "CREATED", "ACTIVE":
			// Bounded reads; the acknowledged receipt survives the retry.
		default:
			return "", commerce.TemporaryError("bundle operation status invalid")
		}
	}
	return "", commerce.TemporaryError("bundle operation pending")
}

func (p *ShopifyProvider) bundleUpdate(ctx context.Context, bundleID, baseComponentID, frameComponentID string, options []map[string]any) error {
	baseOptions, err := p.baseComponentOptions(ctx, baseComponentID, "")
	if err != nil {
		return err
	}
	input := map[string]any{
		"productId": bundleID,
		"components": []map[string]any{
			{"quantity": 1, "productId": baseComponentID, "optionSelections": baseOptions},
			{"quantity": 1, "productId": frameComponentID, "optionSelections": options},
		},
	}
	store, err := commerce.ProductAsyncReceipts(ctx)
	if err != nil {
		return err
	}
	receipt, found, err := store.LoadAsync(ctx, "bundle_update")
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(gqlRequest{Query: docBundleUpdate, Variables: map[string]any{"input": input}})
	intent := fmt.Sprintf("%x", sha256.Sum256(payload))
	if found && receipt.State == "pending" {
		if _, err := p.awaitBundleOperation(ctx, receipt.OperationID); err != nil {
			return err
		}
		if receipt.Intent == intent {
			return nil
		}
	}
	if found && receipt.Intent == intent {
		if receipt.State == "completed" {
			return nil
		}
		if receipt.State == "failed" {
			return p.capabilityError("bundle operation failed")
		}
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
		return p.classifyBundleFailure(err)
	}
	if len(out.ProductBundleUpdate.UserErrors) > 0 {
		return p.capabilityError(out.ProductBundleUpdate.UserErrors[0].Message)
	}
	operationID := out.ProductBundleUpdate.ProductBundleOperation.OperationID
	if operationID == "" {
		return p.capabilityError("bundle operation missing")
	}
	_, err = p.awaitBundleOperation(ctx, operationID)
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
