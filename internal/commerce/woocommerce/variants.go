package woocommerce

// Phase 17 §33-§37: WooCommerce ProductVariant mapping.
//
// MoonLight Product → one Woo product; MoonLight ProductVariant → one
// Woo variation (Woo variable product). Physical inventory is
// VARIANT-specific and must never multiply:
//
//   - Several physical variants (no frame options): one variable product
//     whose variation axes are the variant attributes. Each variation
//     carries SKU = variant SKU, the effective price, and
//     manage_stock=true with the EXACT per-variant availability
//     (SetVariantInventory). The parent keeps no pool (§33-§37).
//   - Exactly one physical variant (with or without frame options): the
//     variant's stock pool IS the parent pool — frame variations stay
//     untracked shared-pool choices (Phase 15 §85) consuming THAT one
//     pool. There is one pool, so nothing multiplies.
//   - Several physical variants AND frame options: Woo cannot layer
//     frame choices on per-variant stock without a (variant × frame)
//     Cartesian variation set whose quantities would multiply the
//     physical stock (Blue 4 + Gold 2 would publish 4·frames and
//     2·frames). The adapter fails with the stable
//     WOO_VARIANT_OPTIONS_CAPABILITY_UNAVAILABLE conflict — never an
//     inventory-multiplying fallback (§37).
//
// Ownership is proven by `_moonlight_variant_id` metadata plus the
// frozen product/provider tuple — never by SKU or labels. Mapping-loss
// recovery (a lost durable mapping while the remote variation exists)
// re-adopts the owned variation by SKU + ownership metadata exactly
// like the Phase 11 Shopify product recovery: never a duplicate.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// variantOptionConflict reports the stable capability conflict when one
// product needs BOTH per-variant tracked stock AND frame options: the
// two Woo representations cannot be layered safely. Fail-safe only —
// stock is never multiplied to paper over the gap.
func variantOptionConflict(product commerce.CommerceProduct) error {
	if len(product.Variants) > 1 && len(product.Configurations) > 0 {
		return commerce.ConflictError(WooVariantOptionsCapabilityCode +
			": frame options cannot layer on per-variant stock without multiplying it")
	}
	return nil
}

// UpsertProductVariants implements commerce.VariantCommerceProvider.
// Variant metadata writes are safe-zero; the service's per-variant
// SetVariantInventory calls restore exact availability afterwards.
func (p *WooCommerceProvider) UpsertProductVariants(ctx context.Context, req commerce.ProductVariantsUpsertRequest) (commerce.ProductVariantsUpsertResult, error) {
	if req.ProviderKey != p.key {
		return commerce.ProductVariantsUpsertResult{}, apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.OperationKey == "" {
		return commerce.ProductVariantsUpsertResult{}, apperr.New(apperr.InvalidInput, "product id and operation key are required")
	}
	if err := variantOptionConflict(req.Product); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	variants := req.Product.Variants
	if len(variants) == 0 {
		return commerce.ProductVariantsUpsertResult{}, commerce.ValidationError("product has no variants")
	}
	if err := validateVariantSet(variants); err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	wooID, err := parseWooID(req.ExternalProductID)
	if err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	remote, err := p.getProduct(ctx, wooID)
	if err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	if !ownershipMatches(remote.metadata(), req.ProductID, req.ProviderKey) {
		return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError(
			"woo product " + strconv.FormatInt(wooID, 10) + " owned by another product or provider")
	}
	result := commerce.ProductVariantsUpsertResult{Variants: map[string]string{}}
	if len(variants) == 1 {
		// The single variant's stock pool IS the parent pool: the parent
		// product is the provider variation identity for mapping and
		// inventory purposes (frame choices share that pool, §85).
		result.Variants[variants[0].VariantID] = canonicalExternalID(wooID)
		return result, nil
	}
	return p.syncWooVariantVariations(ctx, wooID, req)
}

// SetVariantInventory implements commerce.VariantCommerceProvider: the
// exact per-variant availability, never a product aggregate and never a
// sum over frame choices. A single-variant product publishes to the
// parent pool (its one physical inventory); multi-variant products
// publish to the exact variation.
func (p *WooCommerceProvider) SetVariantInventory(ctx context.Context, req commerce.VariantInventoryUpdateRequest) error {
	if req.ProviderKey != p.key {
		return apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.VariantID == "" || req.OperationKey == "" {
		return apperr.New(apperr.InvalidInput, "product id, variant id and operation key are required")
	}
	wooID, err := parseWooID(req.ExternalProductID)
	if err != nil {
		return err
	}
	quantity := req.AvailableQuantity
	if !req.Ready {
		quantity = 0
	}
	if quantity < 0 || quantity > maxWooQuantity {
		return commerce.ValidationError("available quantity out of range")
	}
	if req.ExternalVariantID == req.ExternalProductID {
		// One physical inventory (single variant ± frame choices): the
		// shared parent pool is that variant's pool. Exactly one write —
		// frame choices never add stock.
		return p.SetInventory(ctx, commerce.InventoryUpdateRequest{
			ProviderKey: req.ProviderKey, ProductID: req.ProductID,
			ExternalProductID: req.ExternalProductID,
			AvailableQuantity: quantity, InventoryRevision: req.InventoryRevision,
			CatalogRevision: req.CatalogRevision, PolicyRevision: req.PolicyRevision,
			Ready: req.Ready, OperationKey: req.OperationKey,
		})
	}
	variationID, err := parseWooID(req.ExternalVariantID)
	if err != nil {
		return err
	}
	stock := quantity
	status := wooStockOutOfStock
	if stock > 0 {
		status = wooStockInStock
	}
	payload := wooVariationInventoryPayload{
		ManageStock: true, StockQuantity: stock, StockStatus: status, Backorders: wooBackordersNo,
	}
	var updated wooVariation
	_, err = p.client.do(ctx, http.MethodPut,
		"/products/"+strconv.FormatInt(wooID, 10)+"/variations/"+strconv.FormatInt(variationID, 10),
		nil, payload, &updated)
	if err != nil {
		return err
	}
	// Success must prove which variation was acknowledged: a missing or
	// malformed identity is Temporary (the remote may have applied stock
	// but names no variation), a different identity is a Conflict.
	// Retrying the same update is idempotent; no rollback is attempted.
	if updated.ID == 0 {
		return commerce.TemporaryError("woo variation inventory response missing identity")
	}
	if updated.ID != variationID {
		return commerce.ConflictError(
			"woo variation inventory identity mismatch: want " +
				strconv.FormatInt(variationID, 10) + " got " + strconv.FormatInt(updated.ID, 10))
	}
	return nil
}

// validateVariantSet enforces the adapter-level variant invariants:
// unique identities, unique non-empty SKUs, bounded identities.
func validateVariantSet(variants []commerce.CommerceVariant) error {
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

// syncWooVariantVariations converges one variable product's variation
// set to the desired ProductVariant set. Every remote variation is
// proven by ownership metadata before adoption; unmapped desired
// variants recover by SKU + ownership metadata (mapping loss) or are
// created with lost-response recovery — never duplicated.
func (p *WooCommerceProvider) syncWooVariantVariations(ctx context.Context, wooID int64, req commerce.ProductVariantsUpsertRequest) (commerce.ProductVariantsUpsertResult, error) {
	all, err := p.listAllProductVariations(ctx, canonicalExternalID(wooID))
	if err != nil {
		return commerce.ProductVariantsUpsertResult{}, err
	}
	ownedByVariant := map[string]wooVariation{}
	bySKU := map[string][]wooVariation{}
	for _, variation := range all {
		if variation.SKU != "" {
			bySKU[variation.SKU] = append(bySKU[variation.SKU], variation)
		}
		if id := metaValue(variation.MetaData, metaVariantID); id != "" &&
			metaValue(variation.MetaData, metaProductID) == req.ProductID &&
			metaValue(variation.MetaData, metaProviderKey) == string(p.key) {
			ownedByVariant[id] = variation
		}
	}
	result := commerce.ProductVariantsUpsertResult{Variants: map[string]string{}}
	for _, variant := range req.Product.Variants {
		payload, err := p.buildVariantVariationPayload(req, variant)
		if err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
		current, found := ownedByVariant[variant.VariantID]
		if !found {
			// The durable mapping may name the remote variation while
			// this scan proved nothing yet: trust identity, prove
			// ownership before writing.
			if external := req.ExistingVariants[variant.VariantID]; external != "" {
				mappedID, err := parseWooID(external)
				if err != nil {
					return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("mapped woo variation identity is malformed")
				}
				for _, variation := range all {
					if variation.ID == mappedID {
						current, found = variation, true
						break
					}
				}
				if !found {
					// Remote variation gone: never create a silent
					// replacement and never rewrite the mapping (Phase 11
					// semantics) — operator action is required.
					return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("mapped woo variation no longer exists")
				}
				if !variantVariationAdoptable(current, req.ProductID, req.ProviderKey, variant) {
					return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError("mapped woo variation is not owned by this product or provider")
				}
			}
		}
		if !found {
			// Mapping-loss recovery by SKU + ownership metadata: an
			// existing owned variation is re-adopted — never duplicated.
			candidates := ownedSKUVariations(bySKU[variant.SKU], req.ProductID, req.ProviderKey)
			switch {
			case len(candidates) == 1 && variantVariationAdoptable(candidates[0], req.ProductID, req.ProviderKey, variant):
				current, found = candidates[0], true
			case len(bySKU[variant.SKU]) > 0 && len(candidates) == 0:
				return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError(
					"woo variation sku " + strconv.Quote(variant.SKU) + " is owned by another product or provider")
			case len(candidates) > 1:
				return commerce.ProductVariantsUpsertResult{}, commerce.ConflictError(
					"multiple woo variations share sku " + strconv.Quote(variant.SKU))
			}
		}
		if found {
			updated, err := p.putVariantVariation(ctx, wooID, current.ID, payload)
			if err != nil {
				return commerce.ProductVariantsUpsertResult{}, err
			}
			result.Variants[variant.VariantID] = canonicalExternalID(updated)
			continue
		}
		created, err := p.createVariantVariation(ctx, wooID, variant, payload, req.ProductID)
		if err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
		result.Variants[variant.VariantID] = canonicalExternalID(created)
	}
	// Tombstoned/removed variants stop being sellable (§63): owned
	// variations outside the desired set are hidden by withholding their
	// price and safe-zeroed — Cloud mapping state is never deleted.
	desiredIDs := map[string]bool{}
	for _, variant := range req.Product.Variants {
		desiredIDs[variant.VariantID] = true
	}
	for variantID, current := range ownedByVariant {
		if desiredIDs[variantID] {
			continue
		}
		hidden := commerce.CommerceVariant{VariantID: variantID, SKU: current.SKU, Active: false}
		payload, err := p.buildVariantVariationPayload(req, hidden)
		if err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
		// Keep the existing option selection: hiding must never detach
		// the variation from its axes.
		for _, attribute := range current.Attributes {
			payload.Attributes = append(payload.Attributes, wooVariationAttribute{
				Name: attribute.Name, Option: attribute.Option,
			})
		}
		if _, err := p.putVariantVariation(ctx, wooID, current.ID, payload); err != nil {
			return commerce.ProductVariantsUpsertResult{}, err
		}
	}
	return result, nil
}

// buildVariantVariationPayload renders one tracked variant variation:
// SKU = variant SKU, effective price (override or base), safe-zero
// tracked stock, and the full ownership tuple. Inactive variants are
// hidden by withholding their price (official Woo behavior) with
// metadata retained (§63).
func (p *WooCommerceProvider) buildVariantVariationPayload(req commerce.ProductVariantsUpsertRequest, variant commerce.CommerceVariant) (wooVariationPayload, error) {
	price := ""
	if variant.Active {
		resolved, err := p.variantPrice(req.Product, variant)
		if err != nil {
			return wooVariationPayload{}, err
		}
		price = resolved
	}
	zero := int64(0)
	return wooVariationPayload{
		Attributes:    variantOptionAttributes(variant.Attributes),
		SKU:           strings.TrimSpace(variant.SKU),
		RegularPrice:  price,
		ManageStock:   true,
		StockQuantity: &zero,
		StockStatus:   wooStockOutOfStock,
		Backorders:    wooBackordersNo,
		MetaData: []wooMetaDatum{
			{Key: metaVariantID, Value: variant.VariantID},
			{Key: metaProductID, Value: req.ProductID},
			{Key: metaProviderKey, Value: string(p.key)},
			{Key: metaOperationKey, Value: req.OperationKey},
			{Key: metaCatalogRev, Value: strconv.FormatInt(req.CatalogRevision, 10)},
			{Key: metaPolicyRev, Value: strconv.FormatInt(req.PolicyRevision, 10)},
		},
	}, nil
}

// variantPrice resolves the effective price in the adapter currency:
// the variant's exact int64 minor-unit override when present, otherwise
// the Product base price. No floats, no FX.
func (p *WooCommerceProvider) variantPrice(product commerce.CommerceProduct, variant commerce.CommerceVariant) (string, error) {
	override := variant.PriceEGPMinor
	if strings.EqualFold(p.currency, "USD") {
		override = variant.PriceUSDMinor
	}
	if override != nil {
		return minorToDecimal(*override)
	}
	return configuredPrice(product, p.currency, 0)
}

// variantOptionAttributes renders the variant's combination attributes
// as variation options (display labels only; identity lives in the
// variation ownership metadata).
func variantOptionAttributes(attributes []commerce.CommerceVariantAttribute) []wooVariationAttribute {
	out := make([]wooVariationAttribute, 0, len(attributes))
	for _, attribute := range attributes {
		out = append(out, wooVariationAttribute{
			Name:   displayLabel(attribute.DefinitionNameEN, attribute.DefinitionNameAR),
			Option: displayLabel(attribute.NameEN, attribute.NameAR),
		})
	}
	return out
}

// variantAttributes builds the parent variation axes from the variant
// attribute identities (definition codes ordered by first appearance;
// display values deduplicated like frameAttributes). Labels are display
// only — identity lives in the variation ownership metadata.
func variantAttributes(variants []commerce.CommerceVariant) []wooProductAttribute {
	type axis struct {
		name   string
		values []string
		seen   map[string]bool
	}
	var axes []*axis
	byCode := map[string]*axis{}
	for _, variant := range variants {
		for _, attribute := range variant.Attributes {
			current := byCode[attribute.DefinitionCode]
			if current == nil {
				current = &axis{
					name: displayLabel(attribute.DefinitionNameEN, attribute.DefinitionNameAR),
					seen: map[string]bool{},
				}
				byCode[attribute.DefinitionCode] = current
				axes = append(axes, current)
			}
			value := displayLabel(attribute.NameEN, attribute.NameAR)
			if !current.seen[value] {
				current.seen[value] = true
				current.values = append(current.values, value)
			}
		}
	}
	out := make([]wooProductAttribute, 0, len(axes))
	for position, current := range axes {
		out = append(out, wooProductAttribute{
			Name: current.name, Position: position,
			Visible: true, Variation: true, Options: current.values,
		})
	}
	return out
}

// variantVariationAdoptable proves one remote variation may be adopted
// for the desired variant: the frozen product/provider ownership tuple
// AND either the exact variant identity marker or (mapping-loss
// evidence) the exact SKU. Foreign or contradictory variations are
// never adopted and never overwritten.
func variantVariationAdoptable(variation wooVariation, productID string, key commerce.ProviderKey, variant commerce.CommerceVariant) bool {
	if metaValue(variation.MetaData, metaProductID) != productID ||
		metaValue(variation.MetaData, metaProviderKey) != string(key) {
		return false
	}
	metaID := metaValue(variation.MetaData, metaVariantID)
	if metaID == variant.VariantID {
		return true
	}
	return metaID == "" && variation.SKU != "" && variation.SKU == strings.TrimSpace(variant.SKU)
}

// ownedSKUVariations filters same-SKU candidates down to the ones
// carrying THIS product + provider ownership tuple.
func ownedSKUVariations(candidates []wooVariation, productID string, key commerce.ProviderKey) []wooVariation {
	out := []wooVariation{}
	for _, variation := range candidates {
		if metaValue(variation.MetaData, metaProductID) == productID &&
			metaValue(variation.MetaData, metaProviderKey) == string(key) {
			out = append(out, variation)
		}
	}
	return out
}

// listAllProductVariations returns every remote variation of one
// product. Discovery is complete-or-fail: an exhausted bounded scan is
// a safe error, never proof of absence that could cause a duplicate
// create.
func (p *WooCommerceProvider) listAllProductVariations(ctx context.Context, externalProductID string) ([]wooVariation, error) {
	all := []wooVariation{}
	for page := 1; page <= 4; page++ {
		var batch []wooVariation
		path := "/products/" + externalProductID + "/variations?per_page=100&page=" + strconv.Itoa(page)
		if _, err := p.client.do(ctx, http.MethodGet, path, nil, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
	return nil, commerce.TemporaryError("woo variation discovery: bounded scan exhausted")
}

// putVariantVariation updates one owned variation and proves the
// acknowledged identity.
func (p *WooCommerceProvider) putVariantVariation(ctx context.Context, wooID, variationID int64, payload wooVariationPayload) (int64, error) {
	var updated wooVariation
	_, err := p.client.do(ctx, http.MethodPut,
		"/products/"+strconv.FormatInt(wooID, 10)+"/variations/"+strconv.FormatInt(variationID, 10),
		nil, payload, &updated)
	if err != nil {
		return 0, err
	}
	if updated.ID == 0 {
		return 0, commerce.TemporaryError("woo variation update response missing identity")
	}
	if updated.ID != variationID {
		return 0, commerce.ConflictError(
			"woo variation update identity mismatch: want " +
				strconv.FormatInt(variationID, 10) + " got " + strconv.FormatInt(updated.ID, 10))
	}
	return updated.ID, nil
}

// createVariantVariation creates one tracked variant variation with
// lost-response recovery: a create whose response was lost re-adopts
// the owned variation on retry (by variant identity metadata) — never a
// duplicate.
func (p *WooCommerceProvider) createVariantVariation(ctx context.Context, wooID int64, variant commerce.CommerceVariant, payload wooVariationPayload, productID string) (int64, error) {
	var result struct {
		ID int64 `json:"id"`
	}
	_, err := p.client.do(ctx, http.MethodPost,
		"/products/"+strconv.FormatInt(wooID, 10)+"/variations", nil, payload, &result)
	if err != nil {
		if all, findErr := p.listAllProductVariations(ctx, canonicalExternalID(wooID)); findErr == nil {
			for _, variation := range all {
				if metaValue(variation.MetaData, metaVariantID) == variant.VariantID &&
					metaValue(variation.MetaData, metaProductID) == productID &&
					metaValue(variation.MetaData, metaProviderKey) == string(p.key) {
					return variation.ID, nil
				}
			}
		}
		return 0, err
	}
	if result.ID == 0 {
		return 0, commerce.TemporaryError("woo variation create: missing identity")
	}
	return result.ID, nil
}
