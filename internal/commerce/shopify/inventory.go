package shopify

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

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
// Ordering/compare policy (Phase 11 F2 remediation):
//   - the remote ownership fence (catalog/policy revisions) is checked
//     first: a stale operation aborts retryably before any write;
//   - zero writes CAS from the observed quantity (a stale zero can never
//     blind-zero a drifted or newer positive quantity);
//   - positive writes CAS from the safe-zero this operation establishes
//     (changeFromQuantity 0): an older positive operation can never
//     silently overwrite a proven newer quantity.
//
// Every mutation carries a deterministic idempotency key derived from
// logical intent plus observed physical state. Cached responses are verified
// against a fresh inventory-level read before convergence is reported.
func (p *ShopifyProvider) SetInventory(ctx context.Context, req commerce.InventoryUpdateRequest) error {
	if !commerce.ProductSyncHeld(ctx, p.key, req.ProductID) {
		if p.coordinator == nil {
			return commerce.ValidationError("shopify inventory mutation requires database coordination")
		}
		return p.coordinator.WithProductSync(ctx, p.key, req.ProductID, func(held context.Context) error { return p.SetInventory(held, req) })
	}

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
	fence, err := fenceFrom(values)
	if err != nil {
		return err
	}
	// Stale-start fence: a newer operation's revisions are already
	// stamped on the remote product. Never write stale availability over
	// proven-newer state — the caller retries and re-reads fresh desired
	// state.
	if fence.supersedes(req.CatalogRevision, req.PolicyRevision) {
		return errSuperseded()
	}
	variant, err := recordedVariant(product, values[metafieldManagedVariantID])
	if err != nil {
		return err
	}
	inventoryRevision, err := parseRevision(values["inventory_revision"])
	if err != nil {
		return err
	}
	if inventoryRevision > req.InventoryRevision {
		return errSuperseded()
	}
	baseKey := idempotencyKey("inventory", req.OperationKey)
	if err := p.setManagedQuantity(ctx, variant, quantity, baseKey); err != nil {
		return err
	}
	return p.setOwnership(ctx, product.ID, []map[string]any{{"ownerId": product.ID, "namespace": metafieldNamespace, "key": "inventory_revision", "type": "single_line_text_field", "value": fmt.Sprint(req.InventoryRevision)}})
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

// setManagedQuantity converges managed availability for one variant.
// Every write is compare-and-set against the quantity observed in this
// call's own read, so drift between read and write is always detected:
//
//   - zero writes CAS from the observed quantity: a stale zero can never
//     blind-zero a drifted or newer positive quantity;
//   - positive writes CAS from 0 — the safe-zero this operation
//     establishes. If the level is not at our safe-zero (standalone
//     SetInventory, or external interference), one bounded safe-zero
//     re-establishes the precondition first: the caller is known
//     non-stale from the freshness fence, so zeroing is safe.
func (p *ShopifyProvider) setManagedQuantity(ctx context.Context, variant gqlVariant, quantity int64, baseKey string) error {
	itemID, active, observed, err := p.inventoryState(variant)
	if err != nil {
		return err
	}
	if !active {
		if err := p.activateInventory(ctx, itemID, idempotencyKey("activate-"+itemID, baseKey)); err != nil {
			return err
		}
		loaded, err := p.loadInventoryVariant(ctx, variant.ID)
		if err != nil {
			return err
		}
		variant = loaded
		_, active, observed, err = p.inventoryState(variant)
		if err != nil {
			return err
		}
		if !active {
			return commerce.TemporaryError("shopify inventory activation not physically confirmed")
		}
	}
	// A logical key can replay an old success. Each bounded correction uses a
	// stable key for its observed state/target/attempt, then reads the physical
	// quantity again. No risky caller stage proceeds on a cached response alone.
	for attempt := 0; attempt < 4; attempt++ {
		if observed == quantity {
			return nil
		}
		target := quantity
		if quantity > 0 && observed != 0 {
			target = 0
		}
		from := observed
		version, err := p.inventoryVersion(variant)
		if err != nil {
			return err
		}
		key := idempotencyKey(fmt.Sprintf("physical-%s-%s-%d-%d", itemID, version, observed, target), baseKey)
		if err := p.setAvailable(ctx, itemID, target, &from, key); err != nil {
			return err
		}
		loaded, err := p.loadInventoryVariant(ctx, variant.ID)
		if err != nil {
			return err
		}
		variant = loaded
		_, _, observed, err = p.inventoryState(loaded)
		if err != nil {
			return err
		}
	}
	if observed == quantity {
		return nil
	}
	return commerce.TemporaryError("SHOPIFY_INVENTORY_NOT_CONVERGED")
}

// inventoryState resolves the managed inventory item id, whether it is
// active at the configured location, and the currently observed
// available quantity there. Malformed or untracked items fail closed.
func (p *ShopifyProvider) inventoryState(variant gqlVariant) (string, bool, int64, error) {
	if variant.InventoryItem == nil || variant.InventoryItem.ID == "" {
		return "", false, 0, commerce.ConflictError("shopify managed variant has no inventory item")
	}
	itemID, err := ParseGID(variant.InventoryItem.ID, ResourceInventoryItem)
	if err != nil {
		return "", false, 0, commerce.ConflictError("shopify inventory item identity malformed")
	}
	if !variant.InventoryItem.Tracked {
		return "", false, 0, commerce.ConflictError("shopify managed inventory item is not tracked")
	}
	for _, level := range variant.InventoryItem.Levels.Nodes {
		locationID, err := ParseGID(level.Location.ID, ResourceLocation)
		if err != nil {
			continue
		}
		if strings.EqualFold(locationID, mustLocationID(p.locationGID)) {
			observed := int64(0)
			for _, quantity := range level.Quantities {
				if quantity.Name == "available" {
					observed = quantity.Quantity
				}
			}
			return itemID, true, observed, nil
		}
	}
	return itemID, false, 0, nil
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
		if strings.EqualFold(code, "CONFLICT") || strings.EqualFold(code, "CHANGE_FROM_QUANTITY_STALE") || strings.EqualFold(code, "COMPARE_QUANTITY_STALE") {
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

func (p *ShopifyProvider) loadInventoryVariant(ctx context.Context, gid string) (gqlVariant, error) {
	var out struct {
		Variant *gqlVariant `json:"productVariant"`
	}
	if err := p.client.do(ctx, docInventoryVariant, map[string]any{"id": gid}, &out); err != nil {
		return gqlVariant{}, err
	}
	if out.Variant == nil || out.Variant.ID != gid {
		return gqlVariant{}, commerce.ConflictError("shopify managed variant unavailable")
	}
	return *out.Variant, nil
}

func (p *ShopifyProvider) inventoryVersion(variant gqlVariant) (string, error) {
	if variant.InventoryItem == nil {
		return "", commerce.ConflictError("shopify inventory item missing")
	}
	for _, level := range variant.InventoryItem.Levels.Nodes {
		if level.Location.ID == p.locationGID {
			if _, err := time.Parse(time.RFC3339Nano, level.UpdatedAt); err != nil {
				return "", commerce.TemporaryError("shopify inventory state version missing")
			}
			return level.UpdatedAt, nil
		}
	}
	return "", commerce.ConflictError("shopify inventory location missing")
}
