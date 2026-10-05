package shopify

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// searchSafeSKU matches SKUs that cannot alter Shopify search syntax.
var searchSafeSKU = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// UpsertProduct converges one Shopify product to the desired MoonLight
// state (Phase 11):
//
//   - With an existing mapped id: load the remote product, verify
//     permanent ownership metadata and MoonLight product identity,
//     locate the exact managed SKU variant, safe-zero managed inventory,
//     apply targeted metadata/variant writes (never list-replacement),
//     converge publication state on the configured publication only.
//   - Without one: exact-SKU recovery lookup (search result filtered by
//     exact equality) — empty → create with atomic ownership metadata;
//     exactly one owned candidate → recover/update; foreign or multiple
//     candidates → Conflict.
//
// Call ordering guarantees the safe-zero invariant: after any remote
// product exists, managed inventory is never left stale-positive when a
// later stage fails. The separate CommerceService.SetInventory call
// restores current Phase 5C availability afterwards.
func (p *ShopifyProvider) UpsertProduct(ctx context.Context, req commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	if !commerce.ProductSyncHeld(ctx, p.key, req.ProductID) {
		if p.coordinator == nil {
			return commerce.ProductUpsertResult{}, commerce.ValidationError("shopify product mutation requires database coordination")
		}
		var result commerce.ProductUpsertResult
		err := p.coordinator.WithProductSync(ctx, p.key, req.ProductID, func(held context.Context) error { var err error; result, err = p.UpsertProduct(held, req); return err })
		return result, err
	}

	if req.ProviderKey != p.key {
		return commerce.ProductUpsertResult{}, apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.OperationKey == "" {
		return commerce.ProductUpsertResult{}, apperr.New(apperr.InvalidInput, "product id and operation key are required")
	}
	if req.Product.SKU == "" {
		return commerce.ProductUpsertResult{}, commerce.ValidationError("product sku is required")
	}
	title := localizedPrimary(req.Product.Names)
	if title == "" {
		return commerce.ProductUpsertResult{}, commerce.ValidationError("product title is required in any locale")
	}
	description := localizedDescription(req.Product.Descriptions)
	price, err := configuredPrice(req.Product.Prices, p.currency)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.ensureShopCurrency(ctx); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	desired := desiredContent{title: title, description: description, price: price, sku: req.Product.SKU}
	var result commerce.ProductUpsertResult
	var upsertErr error
	if req.ExistingExternal != nil {
		result, upsertErr = p.updateMapped(ctx, req, desired)
	} else {
		result, upsertErr = p.createWithRecovery(ctx, req, desired)
	}
	if upsertErr != nil {
		return commerce.ProductUpsertResult{}, upsertErr
	}
	// Phase 15 §93-§98: framed Products converge as a bundle whose
	// inventory derives from the tracked base component — one shared
	// papyrus pool across every customer choice. The mapped sellable
	// identity transitions to the bundle parent (documented §116
	// migration); the base component keeps its remote identity and its
	// inventory item untouched.
	if len(req.Product.Configurations) > 0 {
		// Phase 15-R1 F07: the sellable identity becomes the DISTINCT
		// bundle parent only after the base/frame/bundle representation
		// and its ownership records are coherent. The base product keeps
		// its remote identity and tracked inventory item (§116).
		baseGID, err := productGID(result.ExternalProductID)
		if err != nil {
			return commerce.ProductUpsertResult{}, err
		}
		bundleID, identities, err := p.syncShopifyConfigurations(ctx, req, baseGID)
		if err != nil {
			return commerce.ProductUpsertResult{}, err
		}
		if bundleID != "" && bundleID != baseGID {
			// F07: narrow authorized simple→framed sellable transition —
			// ownership/role proven by the bundle adoption above.
			result.ExternalProductID = canonicalFromGID(bundleID)
			result.SellableTransition = true
		}
		result.Configurations = identities
	}
	return result, nil
}

// desiredContent is the provider-owned content set: title, description,
// configured-currency price, stable SKU. Tags, collections, media,
// vendor, product type, SEO, taxonomy, and dimensions are never written.
type desiredContent struct {
	title       string
	description string
	price       string
	sku         string
}

// updateMapped verifies remote ownership of the mapped product, then
// converges content and publication with safe-zero ordering. Missing,
// foreign, or mismatched remote products conflict without any write.
func (p *ShopifyProvider) updateMapped(ctx context.Context, req commerce.ProductUpsertRequest, desired desiredContent) (commerce.ProductUpsertResult, error) {
	externalID, err := CanonicalDecimalID(req.ExistingExternal.ExternalProductID)
	if err != nil {
		return commerce.ProductUpsertResult{}, commerce.ConflictError("mapped shopify product identity is malformed")
	}
	productGID := FormatGID(ResourceProduct, externalID)
	product, err := p.loadProduct(ctx, productGID)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if product == nil {
		// Mapped product missing: never create a replacement, never
		// rewrite the mapping. Operator action is required.
		return commerce.ProductUpsertResult{}, commerce.ConflictError("mapped shopify product no longer exists")
	}
	return p.convergeExisting(ctx, req, desired, product, externalID, true)
}

// convergeExisting runs the mapped convergence against an already-loaded
// owned product (shared by update and recovery paths).
//
// Production callers serialize the complete sequence with PostgreSQL
// coordination. Remote revision checks reject stale prepared work, but are
// not atomic conditions on Product mutations. An already-received remote
// write can outlive cancellation and the database lock; F2 remains open for
// that interruption window (see ADR-0044).
func (p *ShopifyProvider) convergeExisting(ctx context.Context, req commerce.ProductUpsertRequest, desired desiredContent, product *gqlProduct, externalID string, verifyOwnership bool) (commerce.ProductUpsertResult, error) {
	productGID := FormatGID(ResourceProduct, externalID)
	if product.ID != "" {
		responseID, err := CanonicalExternalID(product.ID, ResourceProduct)
		if err != nil || responseID != externalID {
			// Mutation/query response identified a different product
			// than the mapped one: no write, no mapping rewrite.
			return commerce.ProductUpsertResult{}, commerce.ConflictError("shopify product identity mismatch")
		}
	}
	values := ownership(product.Metafields.Nodes)
	if verifyOwnership && !ownershipMatches(values, req.ProductID, req.ProviderKey) {
		return commerce.ProductUpsertResult{}, commerce.ConflictError(
			"shopify product owned by another product or provider")
	}
	fence, err := fenceFrom(values)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	// Stale-start fence: remote revisions newer than this operation's
	// desired revisions prove a newer operation already converged. Abort
	// retryably BEFORE any remote write (including safe-zero): a stale
	// full sequence can never overwrite proven-newer state.
	if fence.supersedes(req.CatalogRevision, req.PolicyRevision) {
		return commerce.ProductUpsertResult{}, errSuperseded()
	}
	variant, err := managedVariant(product, desired.sku, values[metafieldManagedVariantID])
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	variantID, err := parseVariantGID(variant.ID)
	if err != nil {
		return commerce.ProductUpsertResult{}, commerce.ConflictError("shopify managed variant identity malformed")
	}

	// Safe-zero FIRST (CAS-guarded): metadata/publication failures below
	// can never leave stale positive provider stock, and a stale zero can
	// never blind-zero a drifted quantity.
	if err := p.checkFence(ctx, productGID, req); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.setManagedQuantity(ctx, variant, 0, idempotencyKey("safe-zero", req.OperationKey)); err != nil {
		return commerce.ProductUpsertResult{}, err
	}

	// Targeted content writes only: omitted fields are never sent, so
	// manual tags/collections/media/SEO/vendor/product type survive.
	updateInput := map[string]any{
		"id":              productGID,
		"title":           desired.title,
		"descriptionHtml": desired.description,
	}
	if req.Published && !strings.EqualFold(product.Status, "ACTIVE") {
		// Shopify requires ACTIVE before publication. Cross-channel
		// impact is documented in docs/operations/shopify.md.
		updateInput["status"] = "ACTIVE"
	}
	if err := p.checkFence(ctx, productGID, req); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.productUpdate(ctx, updateInput); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.checkFence(ctx, productGID, req); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.updateManagedVariant(ctx, productGID, variant.ID, desired.sku, desired.price); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	// Check freshness before stamping; this is not remote compare-and-set.
	if err := p.checkFence(ctx, productGID, req); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.setOwnership(ctx, productGID, ownershipMetafields(productGID, req, variantID)); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.checkFence(ctx, productGID, req); err != nil {
		return commerce.ProductUpsertResult{}, err
	}

	// Publication is scoped to the configured publication only; other
	// sales channels are never touched.
	if err := p.setPublication(ctx, productGID, req.Published); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	// Detect visible supersession. Returning an error does not undo a remote
	// side effect and is not durable reconciliation.
	if err := p.verifyFence(ctx, productGID, req); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	return commerce.ProductUpsertResult{ExternalProductID: externalID}, nil
}

// checkFence re-reads the remote ownership fence immediately before one
// child write and fails retryably when a newer operation owns the remote
// product.
func (p *ShopifyProvider) checkFence(ctx context.Context, productGID string, req commerce.ProductUpsertRequest) error {
	fence, err := p.loadFence(ctx, productGID)
	if err != nil {
		return err
	}
	if fence.supersedes(req.CatalogRevision, req.PolicyRevision) {
		return errSuperseded()
	}
	return nil
}

// verifyFence re-reads the remote ownership fence and fails retryably
// when a different (newer) operation owns the remote product.
func (p *ShopifyProvider) verifyFence(ctx context.Context, productGID string, req commerce.ProductUpsertRequest) error {
	fence, err := p.loadFence(ctx, productGID)
	if err != nil {
		return err
	}
	if fence.supersedes(req.CatalogRevision, req.PolicyRevision) ||
		fence.supersededBy(req.OperationKey) {
		return errSuperseded()
	}
	return nil
}

// loadFence reads only the ownership metafields: the lightweight
// freshness-fence probe used before every child write.
func (p *ShopifyProvider) loadFence(ctx context.Context, productGID string) (remoteFence, error) {
	var out productQueryResponse
	if err := p.client.do(ctx, docProductQuery, map[string]any{"id": productGID}, &out); err != nil {
		return remoteFence{}, err
	}
	if out.Product == nil {
		return remoteFence{}, commerce.ConflictError("shopify product no longer exists")
	}
	return fenceFrom(ownership(out.Product.Metafields.Nodes))
}

// createWithRecovery runs exact-SKU preflight: empty → create; exactly
// one owned candidate → recover; foreign or multiple → conflict.
func (p *ShopifyProvider) createWithRecovery(ctx context.Context, req commerce.ProductUpsertRequest, desired desiredContent) (commerce.ProductUpsertResult, error) {
	found, err := p.lookupBySKU(ctx, desired.sku)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	switch len(found) {
	case 0:
		return p.createProduct(ctx, req, desired)
	case 1:
		candidate := found[0]
		values := ownership(candidate.Product.Metafields.Nodes)
		if !ownershipMatches(values, req.ProductID, req.ProviderKey) {
			// Foreign or unowned same-SKU remote product: conflict, zero
			// writes. Same-SKU cross-Store safety relies on this.
			return commerce.ProductUpsertResult{}, commerce.ConflictError(
				fmt.Sprintf("sku %q owned by another product or provider", desired.sku))
		}
		productID, err := CanonicalExternalID(candidate.Product.ID, ResourceProduct)
		if err != nil {
			return commerce.ProductUpsertResult{}, commerce.ConflictError("shopify recovery candidate identity malformed")
		}
		// Recovery re-reads full remote state (managed variant and
		// inventory identity included) before converging.
		product, err := p.loadProduct(ctx, FormatGID(ResourceProduct, productID))
		if err != nil {
			return commerce.ProductUpsertResult{}, err
		}
		if product == nil {
			return commerce.ProductUpsertResult{}, commerce.ConflictError("shopify recovery candidate disappeared")
		}
		return p.convergeExisting(ctx, req, desired, product, productID, false)
	default:
		return commerce.ProductUpsertResult{}, commerce.ConflictError(
			fmt.Sprintf("multiple shopify products share sku %q", desired.sku))
	}
}

// createProduct creates the remote product with permanent ownership
// metadata in the same remote logical creation (productSet create path:
// the new resource has no external state to preserve, so list fields
// cannot delete anything unmanaged). Created at managed inventory zero;
// publication is applied afterwards only when desired.
func (p *ShopifyProvider) createProduct(ctx context.Context, req commerce.ProductUpsertRequest, desired desiredContent) (commerce.ProductUpsertResult, error) {
	status := "DRAFT"
	if req.Published {
		status = "ACTIVE"
	}
	input := map[string]any{
		"title":           desired.title,
		"descriptionHtml": desired.description,
		"status":          status,
		"productOptions": []map[string]any{
			{"name": "Title", "values": []map[string]any{{"name": "Default"}}},
		},
		"variants": []map[string]any{{
			"optionValues":  []map[string]any{{"optionName": "Title", "name": "Default"}},
			"sku":           desired.sku,
			"price":         desired.price,
			"inventoryItem": map[string]any{"tracked": true},
		}},
		// Ownership metadata is created atomically with the product:
		// ambiguous-create and mapping-loss recovery can always prove
		// MoonLight identity. The managed variant id is refreshed right
		// after creation (it cannot be known before the remote assigns
		// it).
		"metafields": ownershipMetafields("", req, ""),
	}
	metafields := input["metafields"].([]map[string]any)
	for i := range metafields {
		// ownerId is assigned by Shopify for create-time metafields on
		// the created product; strip the placeholder.
		delete(metafields[i], "ownerId")
	}
	var out productCreateResponse
	if err := p.client.do(ctx, docProductCreate, map[string]any{"input": input}, &out); err != nil {
		// Ambiguous create (transport loss after remote commit) returns
		// a retryable failure; the retry discovers the created product by
		// exact SKU + ownership and recovers it without a second create.
		return commerce.ProductUpsertResult{}, err
	}
	if err := p.failUserErrors("productSet", out.ProductSet.UserErrors); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if out.ProductSet.Product == nil || out.ProductSet.Product.ID == "" {
		return commerce.ProductUpsertResult{}, commerce.TemporaryError("shopify create response missing product id")
	}
	externalID, err := CanonicalExternalID(out.ProductSet.Product.ID, ResourceProduct)
	if err != nil {
		return commerce.ProductUpsertResult{}, commerce.TemporaryError("shopify create response product id malformed")
	}
	productGID := FormatGID(ResourceProduct, externalID)
	variantGID := ""
	variantID := ""
	if len(out.ProductSet.Product.Variants.Nodes) == 1 {
		variantGID = out.ProductSet.Product.Variants.Nodes[0].ID
		variantID, err = parseVariantGID(variantGID)
		if err != nil {
			variantID = ""
			variantGID = ""
		}
	}
	if variantGID == "" {
		// The remote creation occurred but the managed variant is
		// unidentified: retryable (SKU recovery re-reads it).
		return commerce.ProductUpsertResult{}, commerce.TemporaryError("shopify create response missing managed variant")
	}
	if err := p.setOwnership(ctx, productGID, ownershipMetafields(productGID, req, variantID)); err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	// The created managed variant is tracked at quantity zero by Shopify
	// (no quantities are sent at create), so remote managed inventory is
	// safe-zero before any later stage can fail.
	if req.Published {
		if err := p.setPublication(ctx, productGID, true); err != nil {
			return commerce.ProductUpsertResult{}, err
		}
	}
	return commerce.ProductUpsertResult{ExternalProductID: externalID}, nil
}

// loadProduct retrieves one product with ownership metafields and
// variant/inventory detail. Missing products return (nil, nil).
func (p *ShopifyProvider) loadProduct(ctx context.Context, productGID string) (*gqlProduct, error) {
	var out productQueryResponse
	if err := p.client.do(ctx, docProductQuery, map[string]any{"id": productGID}, &out); err != nil {
		return nil, err
	}
	if out.Product == nil {
		return nil, nil
	}
	if out.Product.ID != "" {
		if _, err := CanonicalExternalID(out.Product.ID, ResourceProduct); err != nil {
			return nil, commerce.ConflictError("shopify product identity malformed")
		}
	}
	return out.Product, nil
}

// lookupBySKU searches variants by SKU and filters by exact equality:
// search syntax is never trusted as exact identity. The search term is
// escaped so exotic SKUs cannot alter the search expression (a broken
// expression could hide the true match during recovery), and the page is
// bounded but generous enough that a match is unlikely to be paged out.
func (p *ShopifyProvider) lookupBySKU(ctx context.Context, sku string) ([]gqlVariantWithProduct, error) {
	ctx, cancel := context.WithTimeout(ctx, paginationTimeout)
	defer cancel()
	query := "sku:" + quoteSearchTerm(sku)
	exact := []gqlVariantWithProduct{}
	seen := map[string]bool{}
	var after any
	total := 0
	for page := 0; page < maxConnectionPages; page++ {
		var out variantsBySKUResponse
		if err := p.client.do(ctx, docVariantsBySKU, map[string]any{"query": query, "after": after}, &out); err != nil {
			return nil, err
		}
		total += len(out.ProductVariants.Nodes)
		if total > maxConnectionNodes || len(out.ProductVariants.Nodes) > 50 {
			return nil, paginationError()
		}
		for _, variant := range out.ProductVariants.Nodes {
			if variant.SKU == sku {
				exact = append(exact, variant)
			}
		}
		next, more, err := nextPage(out.ProductVariants.PageInfo, len(out.ProductVariants.Nodes), seen)
		if err != nil {
			return nil, err
		}
		if !more {
			return exact, nil
		}
		after = next
	}
	return nil, paginationError()
}

// quoteSearchTerm renders one SKU for Shopify search syntax. Values
// outside a conservative unquoted charset are double-quoted with
// backslash-escaping so they cannot introduce search operators.
func quoteSearchTerm(sku string) string {
	if searchSafeSKU.MatchString(sku) {
		return sku
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(sku)
	return `"` + escaped + `"`
}

// productUpdate applies targeted product field updates. Omitted fields
// are never sent: manual tags, collections, media, SEO, vendor, product
// type, and unrelated metafields are preserved.
func (p *ShopifyProvider) productUpdate(ctx context.Context, input map[string]any) error {
	var out productUpdateResponse
	if err := p.client.do(ctx, docProductUpdate, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if err := p.failUserErrors("productUpdate", out.ProductUpdate.UserErrors); err != nil {
		return err
	}
	if out.ProductUpdate.Product == nil {
		return commerce.TemporaryError("shopify product update response missing product")
	}
	return nil
}

// updateManagedVariant writes ONLY the MoonLight-managed variant (SKU,
// price). Other variants on the product are never listed and therefore
// never deleted or modified.
func (p *ShopifyProvider) updateManagedVariant(ctx context.Context, productGID, variantGID, sku, price string) error {
	var out variantUpdateResponse
	if err := p.client.do(ctx, docManagedVariantUpdate, map[string]any{
		"productId": productGID,
		"variants": []map[string]any{{
			"id":            variantGID,
			"inventoryItem": map[string]any{"sku": sku},
			"price":         price,
		}},
	}, &out); err != nil {
		return err
	}
	if err := p.failUserErrors("productVariantsBulkUpdate", out.ProductVariantsBulkUpdate.UserErrors); err != nil {
		return err
	}
	if len(out.ProductVariantsBulkUpdate.ProductVariants) != 1 {
		return commerce.TemporaryError("shopify managed variant update response missing variant")
	}
	return nil
}

// setOwnership writes only moonlight-namespace metafields (targeted
// upsert by key). Unrelated external metafields are never deleted.
func (p *ShopifyProvider) setOwnership(ctx context.Context, productGID string, fields []map[string]any) error {
	var out metafieldsSetResponse
	if err := p.client.do(ctx, docMetafieldsSet, map[string]any{"metafields": fields}, &out); err != nil {
		return err
	}
	if err := p.failUserErrors("metafieldsSet", out.MetafieldsSet.UserErrors); err != nil {
		return err
	}
	return nil
}

// setPublication converges publication state on the configured
// publication only. Unrelated publications and channels are untouched;
// both directions are idempotent.
func (p *ShopifyProvider) setPublication(ctx context.Context, productGID string, published bool) error {
	document := docPublish
	payload := "publishablePublish"
	if !published {
		document = docUnpublish
		payload = "publishableUnpublish"
	}
	var out publishResponse
	if err := p.client.do(ctx, document, map[string]any{
		"id":    productGID,
		"input": []map[string]any{{"publicationId": p.publicationID}},
	}, &out); err != nil {
		return err
	}
	var errs []userError
	var id string
	switch payload {
	case "publishablePublish":
		if out.PublishablePublish != nil {
			errs = out.PublishablePublish.UserErrors
			if out.PublishablePublish.Publishable != nil {
				id = out.PublishablePublish.Publishable.ID
			}
		}
	default:
		if out.PublishableUnpublish != nil {
			errs = out.PublishableUnpublish.UserErrors
			if out.PublishableUnpublish.Publishable != nil {
				id = out.PublishableUnpublish.Publishable.ID
			}
		}
	}
	if err := p.failUserErrors(payload, errs); err != nil {
		return err
	}
	if id != "" {
		if responseID, err := CanonicalExternalID(id, ResourceProduct); err != nil || responseID == "" {
			return commerce.ConflictError("shopify publication identity mismatch")
		}
	}
	return nil
}

// localizedPrimary renders the provider title: Arabic name primary,
// English fallback. No automatic translation.
func localizedPrimary(names []commerce.LocalizedName) string {
	fallback := ""
	for _, name := range names {
		switch name.Locale {
		case "ar":
			if name.Name != "" {
				return name.Name
			}
		case "en":
			if fallback == "" {
				fallback = name.Name
			}
		}
	}
	return fallback
}

// localizedDescription renders the provider description: Arabic
// description, English fallback, empty otherwise. Unicode is preserved
// exactly.
func localizedDescription(descriptions map[string]string) string {
	if value, ok := descriptions["ar"]; ok && value != "" {
		return value
	}
	return descriptions["en"]
}

// configuredPrice selects exactly one MoonLight price in the configured
// provider currency. Missing configured currency is validation failure:
// there is no fallback to another currency.
func configuredPrice(prices []commerce.Money, currency string) (string, error) {
	for _, price := range prices {
		if price.Currency != currency {
			continue
		}
		formatted, err := FormatMinorUnits(price.AmountMinor)
		if err != nil {
			return "", commerce.ValidationError("product price is invalid")
		}
		return formatted, nil
	}
	return "", commerce.ValidationError(fmt.Sprintf("product has no %s price", currency))
}
