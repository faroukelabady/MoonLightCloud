package shopify

// Phase 17 §33-§37: Shopify ProductVariant mapping.
//
// MoonLight Product → one Shopify product (or the Phase 15 bundle
// representation); MoonLight ProductVariant → one TRACKED Shopify
// variant owning its own inventory item. Physical inventory is
// VARIANT-specific and must never multiply:
//
//   - Several physical variants (no frame options): the sellable product
//     carries one tracked Shopify variant per MoonLight variant (SKU,
//     exact price) and one inventory level per variant at the configured
//     location — Blue 4 stays 4, Gold 2 stays 2.
//   - Exactly one physical variant (with or without frame options): the
//     variant maps to the tracked base-papyrus managed variant (the
//     bundle component when frames exist). Frame choices remain
//     untracked components/bundle options and consume THAT SAME variant
//     stock — bundle availability derives from component inventory, so
//     all frame choices of one variant share one pool (§33-§37).
//   - Several physical variants AND frame options: the current bundle
//     component mapping cannot layer frame choices on per-variant stock
//     without a (variant × frame) Cartesian set the durable
//     configuration/variant mappings cannot identify safely. The
//     adapter fails with the stable
//     SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE code — never an
//     inventory-multiplying fallback.
//
// Variant ownership is proven by the `moonlight.variant_id` variant
// metafield (F08 pattern) — never SKU alone, never labels. Mapping-loss
// recovery re-adopts the owned remote variant by exact SKU + ownership
// metadata before any create (Phase 11 product recovery pattern): never
// a duplicate. Safe-zero ordering and the remoteFence (catalog/policy
// revisions) apply per variant exactly as for the managed variant.

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

const (
	// metafieldVariantID stamps MoonLight variant identity on the
	// provider variant (variant-level ownership, F08 pattern).
	metafieldVariantID = "variant_id"
	// metafieldVariantInventory is the per-variant inventory revision
	// fence (the product-level fence would mis-supersee interleaved
	// per-variant publishes with independent revisions).
	metafieldVariantInventory = "inventory_revision"
)

// docVariantSet converges one product's option axes and explicit
// variant set (productSet is the repo's create-or-update variant
// surface; the frame component uses the same shape).
const docVariantSet = `mutation MoonlightVariantSet($input: ProductSetInput!) {
  productSet(synchronous: true, input: $input) {
    product {
      id
      options { id name }
      variants(first: 100) { nodes { id } }
    }
    userErrors { field message }
  }
}`

// validateShopifyVariantSet enforces adapter-level variant invariants:
// unique bounded identities, unique non-empty SKUs.
func validateShopifyVariantSet(variants []commerce.CommerceVariant) error {
	seenIDs := map[string]bool{}
	seenSKUs := map[string]bool{}
	for _, variant := range variants {
		if variant.VariantID == "" || len(variant.VariantID) > 200 {
			return commerce.ValidationError("variant id must be 1..200 characters")
		}
		if seenIDs[variant.VariantID] {
			return commerce.ValidationError("duplicate variant id")
		}
		seenIDs[variant.VariantID] = true
		sku := strings.TrimSpace(variant.SKU)
		if sku == "" || len(sku) > 100 {
			return commerce.ValidationError("variant sku must be 1..100 characters")
		}
		if seenSKUs[sku] {
			return commerce.ValidationError("duplicate variant sku")
		}
		seenSKUs[sku] = true
	}
	return nil
}

// UpsertProductVariants implements commerce.VariantCommerceProvider.
// Variant metadata writes are safe-zero; the service's per-variant
// SetVariantInventory calls restore exact availability afterwards.
func (p *ShopifyProvider) UpsertProductVariants(ctx context.Context, req commerce.ProductVariantsUpsertRequest) (commerce.ProductVariantsUpsertResult, error) {
	if !commerce.ProductSyncHeld(ctx, p.key, req.ProductID) {
		if p.coordinator == nil {
			return commerce.ProductVariantsUpsertResult{}, commerce.ValidationError("shopify variant mutation requires database coordination")
		}
		var result commerce.ProductVariantsUpsertResult
		err := p.coordinator.WithProductSync(ctx, p.key, req.ProductID, func(held context.Context) error {
			var err error
			result, err = p.UpsertProductVariants(held, req)
			return err
		})
		return result, err
	}
	if req.ProviderKey != p.key {
		return commerce.ProductVariantsUpsertResult{}, apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.OperationKey == "" {
		return commerce.ProductVariantsUpsertResult{}, apperr.New(apperr.InvalidInput, "product id and operation key are required")
	}
	variants := req.Product.Variants
	if len(variants) == 0 {
		return commerce.ProductVariantsUpsertResult{}, commerce.ValidationError("product has no variants")
	}
	if err := validateShopifyVariantSet(variants); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	// Capability gates: fail safely before any remote write (F06). Frame
	// options cannot layer on several per-variant stock pools without
	// multiplying stock, and Shopify caps products at three option axes.
	if len(variants) > 1 && len(req.Product.Configurations) > 0 {
		return commerce.ProductVariantsUpsertResult{}, p.capabilityError(
			"frame options cannot layer on per-variant stock without multiplying it")
	}
	if len(variants) > 1 {
		if _, _, err := variantAxes(variants); err != nil {
			return commerce.ProductVariantsUpsertResult{}, p.classifyBundleFailure(err)
		}
	}
	if err := p.ensureShopCurrency(ctx); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	product, externalID, err := p.resolveVariantProduct(ctx, req.ProductID, req.ProviderKey, req.ExternalProductID, req.CatalogRevision, req.PolicyRevision)
	if err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	if len(variants) == 1 {
		return p.singleVariantIdentity(ctx, req, product, variants[0])
	}
	return p.convergeShopifyVariants(ctx, req, product, externalID)
}

// resolveVariantProduct resolves the inventory-owning target for
// variant work: the mapped sellable product, or its tracked base
// component when the sellable is the Phase 15 bundle parent (bundle
// inventory derives from the base component — every frame choice
// consumes that same pool). Ownership is proven before any write.
func (p *ShopifyProvider) resolveVariantProduct(ctx context.Context, productID string, providerKey commerce.ProviderKey, externalProductID string, catalogRevision, policyRevision int64) (*gqlProduct, string, error) {
	externalID, err := CanonicalDecimalID(externalProductID)
	if err != nil {
		return nil, "", commerce.ConflictError("shopify product identity is malformed")
	}
	product, err := p.loadProduct(ctx, FormatGID(ResourceProduct, externalID))
	if err != nil {
		return nil, "", err
	}
	if product == nil {
		return nil, "", commerce.ConflictError("mapped shopify product no longer exists")
	}
	if metafieldValue(product.Metafields.Nodes, keyRole) == roleBundleParent {
		baseGID := metafieldValue(product.Metafields.Nodes, keyBaseComponentID)
		if baseGID == "" {
			return nil, "", commerce.ConflictError("bundle base component identity missing")
		}
		product, err = p.loadProduct(ctx, baseGID)
		if err != nil {
			return nil, "", err
		}
		if product == nil {
			return nil, "", commerce.ConflictError("bundle base component no longer exists")
		}
		externalID, err = CanonicalExternalID(product.ID, ResourceProduct)
		if err != nil {
			return nil, "", err
		}
	}
	values := ownership(product.Metafields.Nodes)
	if !ownershipMatches(values, productID, providerKey) {
		return nil, "", commerce.ConflictError("shopify product owned by another product or provider")
	}
	fence, err := fenceFrom(values)
	if err != nil {
		return nil, "", err
	}
	if fence.supersedes(catalogRevision, policyRevision) {
		return nil, "", errSuperseded()
	}
	return product, externalID, nil
}

// singleVariantIdentity resolves the one tracked Shopify variant of a
// single-variant product (the managed variant — the base-papyrus pool
// when frames exist) and stamps durable variant ownership on it. SKU
// and price stay the product flow's responsibility (the managed variant
// carries the variant SKU); this call never duplicates a variant.
func (p *ShopifyProvider) singleVariantIdentity(ctx context.Context, req commerce.ProductVariantsUpsertRequest, product *gqlProduct, variant commerce.CommerceVariant) (commerce.ProductVariantsUpsertResult, error) {
	values := ownership(product.Metafields.Nodes)
	managed, err := managedVariant(product, variant.SKU, values[metafieldManagedVariantID])
	if err != nil {
		// Phase 17-R0 (R08): a recorded managed identity that no longer
		// matches the unique exact-SKU variant is a STALE transition
		// leftover (crash between the 1↔N shape change and its tombstone)
		// ONLY when the variant-scoped ownership stamp proves the
		// exact-SKU variant is ours. Then it is repaired below — never
		// blindly adopted. Every other mismatch stays fail-closed.
		if candidate, cerr := exactSKUVariant(product, variant.SKU); cerr == nil &&
			metafieldValue(candidate.Metafields.Nodes, metafieldVariantID) == variant.VariantID {
			managed = candidate
		} else {
			return commerce.ProductVariantsUpsertResult{}, err
		}
	}
	stamp := metafieldValue(managed.Metafields.Nodes, metafieldVariantID)
	if stamp != "" && stamp != variant.VariantID {
		return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("shopify managed variant owned by another variant")
	}
	if stamp == "" {
		if err := p.setOwnership(ctx, managed.ID, []map[string]any{{
			"ownerId": managed.ID, "namespace": metafieldNamespace, "key": metafieldVariantID,
			"type": "single_line_text_field", "value": variant.VariantID,
		}}); err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
	}
	decimalID, err := CanonicalExternalID(managed.ID, ResourceProductVariant)
	if err != nil {
		return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("shopify managed variant identity malformed")
	}
	// (Re)stamp the product-level managed-variant identity when it is
	// absent, tombstoned, or stale: completing the N→1 transition here
	// keeps product-level flows (managed-variant inventory) coherent even
	// when only the variant reconciliation ran.
	if values[metafieldManagedVariantID] != decimalID {
		if err := p.setOwnership(ctx, product.ID, []map[string]any{{
			"ownerId": product.ID, "namespace": metafieldNamespace, "key": metafieldManagedVariantID,
			"type": "single_line_text_field", "value": decimalID,
		}}); err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
	}
	return commerce.ProductVariantsUpsertResult{Variants: map[string]string{variant.VariantID: decimalID}}, nil
}

// exactSKUVariant resolves the single variant carrying an exact SKU on
// the product (the managed-variant shape's identity tuple). Zero or
// several matches fail closed.
func exactSKUVariant(product *gqlProduct, sku string) (gqlVariant, error) {
	if product == nil {
		return gqlVariant{}, commerce.ConflictError("shopify product missing")
	}
	var exact []gqlVariant
	for _, variant := range product.Variants.Nodes {
		if variant.SKU == sku && sku != "" {
			exact = append(exact, variant)
		}
	}
	if len(exact) != 1 {
		return gqlVariant{}, commerce.ConflictError(
			fmt.Sprintf("shopify product has %d exact sku matches for the managed variant", len(exact)))
	}
	return exact[0], nil
}

// clearStaleManagedVariant durably tombstones the product-level
// managed_variant_id metafield when the multi-variant shape supersedes
// the managed-variant shape (Phase 17-R0, R08). The write is an empty
// value through metafieldsSet (the documented tombstone): the key must
// never keep pointing at a variant that no longer plays the managed role
// across a 1↔N transition. Idempotent: an already-empty or absent key is
// never rewritten.
func (p *ShopifyProvider) clearStaleManagedVariant(ctx context.Context, productGID, current string) error {
	if current == "" {
		return nil
	}
	return p.setOwnership(ctx, productGID, []map[string]any{{
		"ownerId": productGID, "namespace": metafieldNamespace, "key": metafieldManagedVariantID,
		"type": "single_line_text_field", "value": "",
	}})
}

// convergeShopifyVariants converges one product's option axes and its
// explicit tracked variant set to the desired ProductVariant set.
// Safe-zero ordering per variant precedes content writes; identity is
// established from variant ownership metadata (SKU + ownership metadata
// re-adopts lost mappings; creates are never duplicated).
func (p *ShopifyProvider) convergeShopifyVariants(ctx context.Context, req commerce.ProductVariantsUpsertRequest, product *gqlProduct, externalID string) (commerce.ProductVariantsUpsertResult, error) {
	productGID := FormatGID(ResourceProduct, externalID)
	axes, axisCodes, err := variantAxes(req.Product.Variants)
	if err != nil {
		return commerce.ProductVariantsUpsertResult{}, p.classifyBundleFailure(err)
	}
	owned := map[string]gqlVariant{}
	bySKU := map[string]gqlVariant{}
	for _, variant := range product.Variants.Nodes {
		if id := metafieldValue(variant.Metafields.Nodes, metafieldVariantID); id != "" {
			owned[id] = variant
		}
		if variant.SKU != "" {
			bySKU[variant.SKU] = variant
		}
	}
	// Safe-zero FIRST (CAS-guarded, per variant): metadata/identity
	// failures below can never leave stale positive provider stock, and
	// a stale zero can never blind-zero a drifted quantity.
	for _, variant := range req.Product.Variants {
		current, ok := owned[variant.VariantID]
		if !ok {
			continue
		}
		if err := p.checkVariantFence(ctx, productGID, req.CatalogRevision, req.PolicyRevision); err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
		if err := p.setManagedQuantity(ctx, current, 0,
			idempotencyKey("variant-safe-zero-"+variant.VariantID, req.OperationKey)); err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
	}
	variantInputs := make([]map[string]any, 0, len(req.Product.Variants))
	for _, variant := range req.Product.Variants {
		input, err := p.variantInput(req, variant, owned, bySKU, axisCodes)
		if err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
		variantInputs = append(variantInputs, input)
	}
	if err := p.checkVariantFence(ctx, productGID, req.CatalogRevision, req.PolicyRevision); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	var out struct {
		ProductSet struct {
			Product struct {
				ID string `json:"id"`
			} `json:"product"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"productSet"`
	}
	input := map[string]any{
		"id":             productGID,
		"productOptions": axes,
		"variants":       variantInputs,
	}
	if err := p.classifyBundleFailure(p.client.do(ctx, docVariantSet, map[string]any{"input": input}, &out)); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	if len(out.ProductSet.UserErrors) > 0 {
		return commerce.ProductVariantsUpsertResult{}, p.capabilityError(
			p.client.safeMessage(out.ProductSet.UserErrors[0].Message))
	}
	if out.ProductSet.Product.ID != "" {
		if responseID, err := CanonicalExternalID(out.ProductSet.Product.ID, ResourceProduct); err != nil || responseID != externalID {
			return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("shopify product identity mismatch")
		}
	}
	if err := p.checkVariantFence(ctx, productGID, req.CatalogRevision, req.PolicyRevision); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	// Phase 17-R0 (R08): the multi-variant shape has NO managed variant.
	// Any product-level managed_variant_id left over from a previous
	// single-variant shape is durably tombstoned (empty value) here so it
	// can never drive adoption or mutation after a 1↔N transition —
	// restart-safe because every reconcile re-runs this convergence.
	if err := p.clearStaleManagedVariant(ctx, productGID,
		metafieldValue(product.Metafields.Nodes, metafieldManagedVariantID)); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	return p.establishShopifyVariantIdentity(ctx, productGID, req)
}

// variantInput builds one entry of the explicit variant set: identity
// adopted from durable mapping, variant ownership metadata, or SKU +
// ownership recovery — otherwise a create (never a duplicate).
func (p *ShopifyProvider) variantInput(req commerce.ProductVariantsUpsertRequest, variant commerce.CommerceVariant, owned map[string]gqlVariant, bySKU map[string]gqlVariant, axisCodes []string) (map[string]any, error) {
	price, err := p.variantPrice(req.Product, variant)
	if err != nil {
		return nil, err
	}
	optionValues, err := variantOptionValues(variant, axisCodes)
	if err != nil {
		return nil, p.classifyBundleFailure(err)
	}
	input := map[string]any{
		"optionValues":  optionValues,
		"sku":           strings.TrimSpace(variant.SKU),
		"price":         price,
		"inventoryItem": map[string]any{"tracked": true},
		"metafields": []map[string]any{{
			"namespace": metafieldNamespace, "key": metafieldVariantID,
			"value": variant.VariantID, "type": "single_line_text_field",
		}},
	}
	adopted := ""
	if current, ok := owned[variant.VariantID]; ok {
		adopted = current.ID
	} else if external := req.ExistingVariants[variant.VariantID]; external != "" {
		// Durable mapping names the remote variant: prove it still lives
		// on this product before adopting. A mapped variant that vanished
		// never gets a silent replacement (Phase 11 semantics).
		wantID, err := ParseGID(FormatGID(ResourceProductVariant, external), ResourceProductVariant)
		if err != nil {
			return nil, commerce.ConflictError("mapped shopify variant identity is malformed")
		}
		for _, current := range bySKU {
			if id, err := ParseGID(current.ID, ResourceProductVariant); err == nil && id == wantID {
				adopted = current.ID
				break
			}
		}
		if adopted == "" {
			for _, current := range owned {
				if id, err := ParseGID(current.ID, ResourceProductVariant); err == nil && id == wantID {
					adopted = current.ID
					break
				}
			}
		}
		if adopted == "" {
			return nil, commerce.ConflictError("mapped shopify variant no longer exists")
		}
	}
	if adopted == "" {
		// Mapping-loss recovery: an existing variant with our ownership
		// metadata and the exact SKU is re-adopted — never duplicated.
		if current, ok := bySKU[strings.TrimSpace(variant.SKU)]; ok {
			stamp := metafieldValue(current.Metafields.Nodes, metafieldVariantID)
			if stamp == "" || stamp == variant.VariantID {
				adopted = current.ID
			} else {
				return nil, commerce.ConflictError("shopify variant sku owned by another variant")
			}
		}
	}
	if adopted != "" {
		input["id"] = adopted
	}
	return input, nil
}

// establishShopifyVariantIdentity establishes and CONFIRMS the
// ownership-proven identity of every desired variant after convergence
// (F08 pattern): identity comes from the variant_id metadata stamp —
// never labels, never array position. A stamp-less created variant is
// stamped once through exact-SKU establishment (the identity-backed
// tuple), and an empty or partial map is a bounded failure — never
// synchronized success.
func (p *ShopifyProvider) establishShopifyVariantIdentity(ctx context.Context, productGID string, req commerce.ProductVariantsUpsertRequest) (commerce.ProductVariantsUpsertResult, error) {
	product, err := p.loadProduct(ctx, productGID)
	if err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	if product == nil {
		return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("shopify product missing")
	}
	identities := map[string]string{}
	bySKU := map[string]gqlVariant{}
	for _, variant := range product.Variants.Nodes {
		if variant.SKU != "" {
			bySKU[variant.SKU] = variant
		}
		id := metafieldValue(variant.Metafields.Nodes, metafieldVariantID)
		if id == "" {
			continue
		}
		if _, duplicate := identities[id]; duplicate {
			return commerce.ProductVariantsUpsertResult{}, p.capabilityError("ambiguous variant identity")
		}
		identities[id] = variant.ID
	}
	var stamps []map[string]any
	for _, variant := range req.Product.Variants {
		if identities[variant.VariantID] != "" {
			continue
		}
		current, ok := bySKU[strings.TrimSpace(variant.SKU)]
		if !ok {
			return commerce.ProductVariantsUpsertResult{}, p.capabilityError("variant identity incomplete")
		}
		if stamp := metafieldValue(current.Metafields.Nodes, metafieldVariantID); stamp != "" {
			return commerce.ProductVariantsUpsertResult{}, p.capabilityError("variant identity incomplete")
		}
		stamps = append(stamps, map[string]any{
			"id": current.ID, "metafields": []map[string]any{{
				"namespace": metafieldNamespace, "key": metafieldVariantID,
				"value": variant.VariantID, "type": "single_line_text_field",
			}},
		})
		identities[variant.VariantID] = current.ID
	}
	if len(stamps) > 0 {
		var out variantUpdateResponse
		if err := p.client.do(ctx, docVariantPrices, map[string]any{
			"productId": productGID, "variants": stamps,
		}, &out); err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
		if err := p.failUserErrors("productVariantsBulkUpdate", out.ProductVariantsBulkUpdate.UserErrors); err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
	}
	result := commerce.ProductVariantsUpsertResult{Variants: map[string]string{}}
	for _, variant := range req.Product.Variants {
		decimalID, err := CanonicalExternalID(identities[variant.VariantID], ResourceProductVariant)
		if err != nil {
			return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("shopify variant identity malformed")
		}
		result.Variants[variant.VariantID] = decimalID
	}
	return result, nil
}

// SetVariantInventory sets provider-facing availability for ONE
// MoonLight variant's tracked Shopify variant at the configured
// location only (Phase 17): the exact per-variant derived quantity —
// never a product aggregate, never a sum over frame choices. Ordering
// and compare policy mirror SetInventory (fence first, CAS writes,
// deterministic idempotency keys), keyed per variant's inventory item.
func (p *ShopifyProvider) SetVariantInventory(ctx context.Context, req commerce.VariantInventoryUpdateRequest) error {
	if !commerce.ProductSyncHeld(ctx, p.key, req.ProductID) {
		if p.coordinator == nil {
			return commerce.ValidationError("shopify variant inventory mutation requires database coordination")
		}
		return p.coordinator.WithProductSync(ctx, p.key, req.ProductID, func(held context.Context) error {
			return p.SetVariantInventory(held, req)
		})
	}
	if req.ProviderKey != p.key {
		return apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.VariantID == "" || req.OperationKey == "" {
		return apperr.New(apperr.InvalidInput, "product id, variant id and operation key are required")
	}
	quantity := req.AvailableQuantity
	if !req.Ready || quantity < 0 {
		quantity = 0
	}
	if quantity > math.MaxInt32 {
		return commerce.ValidationError("availability exceeds shopify quantity range")
	}
	product, _, err := p.resolveVariantProduct(ctx, req.ProductID, req.ProviderKey, req.ExternalProductID, req.CatalogRevision, req.PolicyRevision)
	if err != nil {
		return err
	}
	wantVariantID, err := ParseGID(FormatGID(ResourceProductVariant, req.ExternalVariantID), ResourceProductVariant)
	if err != nil {
		return commerce.ConflictError("shopify variant identity is malformed")
	}
	var target *gqlVariant
	for i := range product.Variants.Nodes {
		id, err := ParseGID(product.Variants.Nodes[i].ID, ResourceProductVariant)
		if err != nil {
			continue
		}
		if id == wantVariantID {
			target = &product.Variants.Nodes[i]
			break
		}
	}
	if target == nil {
		return commerce.ConflictError("shopify mapped variant not found on mapped product")
	}
	if stamp := metafieldValue(target.Metafields.Nodes, metafieldVariantID); stamp != "" && stamp != req.VariantID {
		return commerce.ConflictError("shopify variant owned by another variant")
	}
	// Per-variant inventory revision fence: a newer inventory snapshot
	// for THIS variant is never overwritten by a stale one.
	inventoryRevision, err := parseRevision(metafieldValue(target.Metafields.Nodes, metafieldVariantInventory))
	if err != nil {
		return err
	}
	if inventoryRevision > req.InventoryRevision {
		return errSuperseded()
	}
	baseKey := idempotencyKey("variant-inventory", req.OperationKey)
	if err := p.setManagedQuantity(ctx, *target, quantity, baseKey); err != nil {
		return err
	}
	return p.setOwnership(ctx, target.ID, []map[string]any{{
		"ownerId": target.ID, "namespace": metafieldNamespace, "key": metafieldVariantInventory,
		"type": "single_line_text_field", "value": fmt.Sprint(req.InventoryRevision),
	}})
}

// maxVariantAxes is the Shopify product option bound: one option per
// variant attribute definition, at most three.
const maxVariantAxes = 3

// checkVariantFence mirrors checkFence for variant operations: it
// re-reads the remote ownership fence immediately before one child
// write and fails retryably when a newer operation owns the remote
// product.
func (p *ShopifyProvider) checkVariantFence(ctx context.Context, productGID string, catalogRevision, policyRevision int64) error {
	fence, err := p.loadFence(ctx, productGID)
	if err != nil {
		return err
	}
	if fence.supersedes(catalogRevision, policyRevision) {
		return errSuperseded()
	}
	return nil
}

// variantAxes builds the product option axes from the variant
// combination attributes: one option per definition code (identity
// keyed). Display labels are disambiguated with their stable codes
// (F08): equal labels never merge distinct identities. The second
// return is the ordered definition-code identity of the axes.
func variantAxes(variants []commerce.CommerceVariant) ([]map[string]any, []string, error) {
	type axis struct {
		code   string
		name   string
		values []string
		seen   map[string]bool
	}
	axes := []*axis{}
	byCode := map[string]*axis{}
	for _, variant := range variants {
		for _, attribute := range variant.Attributes {
			current := byCode[attribute.DefinitionCode]
			if current == nil {
				current = &axis{
					code: attribute.DefinitionCode,
					name: displayLabel(attribute.DefinitionNameEN, attribute.DefinitionNameAR) + " (" + attribute.DefinitionCode + ")",
					seen: map[string]bool{},
				}
				byCode[attribute.DefinitionCode] = current
				axes = append(axes, current)
			}
			value := variantValueLabel(attribute)
			if !current.seen[value] {
				current.seen[value] = true
				current.values = append(current.values, value)
			}
		}
	}
	if len(axes) == 0 {
		return nil, nil, commerce.ValidationError("shopify multi-variant products require variant option attributes")
	}
	if len(axes) > maxVariantAxes {
		return nil, nil, commerce.ValidationError(fmt.Sprintf(
			"shopify variant options exceed the %d-axis product limit", maxVariantAxes))
	}
	out := make([]map[string]any, 0, len(axes))
	codes := make([]string, 0, len(axes))
	for _, current := range axes {
		out = append(out, map[string]any{"name": current.name, "values": optionValueList(current.values)})
		codes = append(codes, current.code)
	}
	return out, codes, nil
}

// variantOptionValues renders one variant's option selections. Every
// axis must be covered exactly once; a missing selection is a bounded
// capability refusal, never a guessed value.
func variantOptionValues(variant commerce.CommerceVariant, codes []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		selection := ""
		for _, attribute := range variant.Attributes {
			if attribute.DefinitionCode != code {
				continue
			}
			if selection != "" {
				return nil, commerce.ValidationError("duplicate variant attribute definition")
			}
			selection = variantValueLabel(attribute)
		}
		if selection == "" {
			return nil, commerce.ValidationError("variant attribute coverage incomplete")
		}
		out = append(out, map[string]any{"optionName": axisName(code, variant), "name": selection})
	}
	return out, nil
}

// axisName resolves the rendered option name for one definition code
// from the variant's own attributes (same disambiguation as variantAxes).
func axisName(code string, variant commerce.CommerceVariant) string {
	for _, attribute := range variant.Attributes {
		if attribute.DefinitionCode == code {
			return displayLabel(attribute.DefinitionNameEN, attribute.DefinitionNameAR) + " (" + attribute.DefinitionCode + ")"
		}
	}
	return code
}

// variantValueLabel renders one option value with its stable code
// (F08: labels never collapse distinct identities).
func variantValueLabel(attribute commerce.CommerceVariantAttribute) string {
	return displayLabel(attribute.NameEN, attribute.NameAR) + " (" + attribute.ValueCode + ")"
}

// variantPrice resolves the effective price in the adapter currency:
// the variant's exact int64 minor-unit override when present, otherwise
// the Product base price. No floats, no FX.
func (p *ShopifyProvider) variantPrice(product commerce.CommerceProduct, variant commerce.CommerceVariant) (string, error) {
	override := variant.PriceEGPMinor
	if strings.EqualFold(p.currency, "USD") {
		override = variant.PriceUSDMinor
	}
	if override != nil {
		return FormatMinorUnits(*override)
	}
	return configuredPrice(product.Prices, p.currency)
}
