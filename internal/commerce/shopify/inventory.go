package shopify

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// SetInventory sets provider-facing availability for the mapped
// product's MoonLight-managed variant at the configured location only.
// Quantity is the frozen Phase 5C/9 derived availability consumed
// verbatim (never recomputed here); not-ready forces zero. Only the
// managed inventory item's "available" quantity at the configured
// location is ever mutated: on-hand, committed, reserved, incoming, and
// other locations are untouched.
//
// Compare-and-set policy (Phase 11 §72):
//   - zero writes are unconditional (changeFromQuantity null): zero is
//     always safe to write and can never oversell;
//   - positive writes are conditional on the remote still holding the
//     safe-zero this logical operation established
//     (changeFromQuantity 0). An older positive operation can therefore
//     never silently overwrite a proven newer quantity: the write fails
//     and a retry reconverges to fresh desired state.
//
// Every mutation carries a deterministic idempotency key derived from
// the frozen MoonLight operation identity: identical desired state
// replays as one remote write, changed desired state gets a new key.
func (p *ShopifyProvider) SetInventory(ctx context.Context, req commerce.InventoryUpdateRequest) error {
	if req.ProviderKey != p.key {
		return apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.OperationKey == "" {
		return apperr.New(apperr.InvalidInput, "product id and operation key are required")
	}
	externalID, err := CanonicalDecimalID(req.ExternalProductID)
	if err != nil {
		return commerce.ConflictError("shopify product identity is malformed")
	}
	quantity := req.AvailableQuantity
	if !req.Ready || quantity < 0 {
		quantity = 0
	}
	if quantity > math.MaxInt32 {
		return commerce.ValidationError("availability exceeds shopify quantity range")
	}

	product, err := p.loadProduct(ctx, FormatGID(ResourceProduct, externalID))
	if err != nil {
		return err
	}
	if product == nil {
		return commerce.ConflictError("mapped shopify product no longer exists")
	}
	values := ownership(product.Metafields.Nodes)
	if !ownershipMatches(values, req.ProductID, req.ProviderKey) {
		return commerce.ConflictError("shopify product owned by another product or provider")
	}
	variant, err := recordedVariant(product, values[metafieldManagedVariantID])
	if err != nil {
		return err
	}
	baseKey := idempotencyKey("inventory", req.OperationKey)
	return p.setManagedQuantity(ctx, variant, quantity, baseKey)
}

// recordedVariant resolves the MoonLight-managed variant by the durable
// managed-variant identity recorded in ownership metadata and verifies
// it still belongs to this product. A missing or ambiguous identity
// fails closed: foreign and manual variants are never mutated.
func recordedVariant(product *gqlProduct, recordedVariantID string) (gqlVariant, error) {
	if product == nil {
		return gqlVariant{}, commerce.ConflictError("shopify product missing")
	}
	if recordedVariantID == "" {
		return gqlVariant{}, commerce.ConflictError("shopify managed variant identity missing")
	}
	for _, variant := range product.Variants.Nodes {
		id, err := ParseGID(variant.ID, ResourceProductVariant)
		if err != nil {
			continue
		}
		if id == recordedVariantID {
			return variant, nil
		}
	}
	return gqlVariant{}, commerce.ConflictError("shopify managed variant not found on mapped product")
}

// setManagedQuantity converges managed availability for one variant:
// activation at the configured location first (initial available 0),
// then the absolute quantity write.
//
// changeFrom semantics are decided here: zero is written unconditionally,
// positive writes require the remote to still hold 0 (the safe-zero this
// logical operation established).
func (p *ShopifyProvider) setManagedQuantity(ctx context.Context, variant gqlVariant, quantity int64, baseKey string) error {
	itemID, active, err := p.inventoryState(variant)
	if err != nil {
		return err
	}
	if !active {
		if err := p.activateInventory(ctx, itemID, baseKey+"-activate"); err != nil {
			return err
		}
	}
	var changeFrom *int64
	if quantity > 0 {
		zero := int64(0)
		changeFrom = &zero
	}
	return p.setAvailable(ctx, itemID, quantity, changeFrom, baseKey+"-set")
}

// inventoryState resolves the managed inventory item id and whether it
// is active at the configured location. Malformed or untracked items
// fail closed.
func (p *ShopifyProvider) inventoryState(variant gqlVariant) (string, bool, error) {
	if variant.InventoryItem == nil || variant.InventoryItem.ID == "" {
		return "", false, commerce.ConflictError("shopify managed variant has no inventory item")
	}
	itemID, err := ParseGID(variant.InventoryItem.ID, ResourceInventoryItem)
	if err != nil {
		return "", false, commerce.ConflictError("shopify inventory item identity malformed")
	}
	if !variant.InventoryItem.Tracked {
		return "", false, commerce.ConflictError("shopify managed inventory item is not tracked")
	}
	for _, level := range variant.InventoryItem.Levels.Nodes {
		locationID, err := ParseGID(level.Location.ID, ResourceLocation)
		if err != nil {
			continue
		}
		if strings.EqualFold(locationID, mustLocationID(p.locationGID)) {
			return itemID, true, nil
		}
	}
	return itemID, false, nil
}

// mustLocationID returns the canonical decimal of the configured
// location GID. Validated at construction; defensive fallback keeps this
// helper total.
func mustLocationID(gid string) string {
	id, err := ParseGID(gid, ResourceLocation)
	if err != nil {
		return ""
	}
	return id
}

// activateInventory activates the managed inventory item at the
// configured location with initial available quantity 0 (safe-zero
// first). Idempotent under the derived Shopify idempotency key.
func (p *ShopifyProvider) activateInventory(ctx context.Context, itemID, idemKey string) error {
	var out inventoryActivateResponse
	if err := p.client.do(ctx, docInventoryActivate, map[string]any{
		"inventoryItemId": FormatGID(ResourceInventoryItem, itemID),
		"locationId":      p.locationGID,
		"idempotencyKey":  idemKey,
	}, &out); err != nil {
		return err
	}
	if err := p.failUserErrors("inventoryActivate", out.InventoryActivate.UserErrors); err != nil {
		return err
	}
	if out.InventoryActivate.InventoryLevel == nil {
		return commerce.TemporaryError("shopify inventory activation response missing level")
	}
	return nil
}

// setAvailable performs the absolute "available" write at the
// configured location with compare-and-set and the derived idempotency
// key. A compare mismatch is retryable: retrying reconverges to fresh
// desired state instead of overwriting newer proven state.
func (p *ShopifyProvider) setAvailable(ctx context.Context, itemID string, quantity int64, changeFrom *int64, idemKey string) error {
	entry := map[string]any{
		"inventoryItemId": FormatGID(ResourceInventoryItem, itemID),
		"locationId":      p.locationGID,
		"quantity":        quantity,
	}
	if changeFrom != nil {
		entry["changeFromQuantity"] = *changeFrom
	} else {
		entry["changeFromQuantity"] = nil
	}
	var out inventorySetResponse
	if err := p.client.do(ctx, docInventorySet, map[string]any{
		"input": map[string]any{
			"name":       "available",
			"reason":     "correction",
			"quantities": []map[string]any{entry},
		},
		"idempotencyKey": idemKey,
	}, &out); err != nil {
		return err
	}
	payload := out.InventorySetQuantities
	if len(payload.UserErrors) > 0 {
		first := payload.UserErrors[0]
		code := boundField(p.client.scrub.scrub(first.Code), codeLimit)
		message := boundField(p.client.scrub.scrub(first.Message), messageLimit)
		if strings.EqualFold(code, "CONFLICT") {
			// Compare-and-set detected a concurrent quantity change:
			// retryable, because the caller's retry recomputes fresh
			// desired state before writing again.
			return commerce.TemporaryError(fmt.Sprintf(
				"shopify inventory compare mismatch %s: %s", code, message))
		}
		return commerce.ValidationError(fmt.Sprintf(
			"shopify inventorySetQuantities user error %s: %s", code, message))
	}
	if payload.InventoryAdjustmentGroup == nil {
		return commerce.TemporaryError("shopify inventory response missing adjustment group")
	}
	return nil
}
