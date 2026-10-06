package woocommerce

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

// WooCommerceProvider is the first concrete CommerceProvider: WooCommerce
// REST API v3 over HTTPS with HTTP Basic Auth. Simple products only; no
// taxonomy, images, orders, webhooks, or jobs. Construction performs no
// network I/O, so Cloud starts even when Woo is unreachable.
type WooCommerceProvider struct {
	key      commerce.ProviderKey
	currency string
	dimUnit  string
	client   *Client
}

// NewWooCommerceProvider validates configuration eagerly (no deferred
// failures at first sync) and builds the bounded transport. The injected
// *http.Client may carry a test TLS transport; redirect policy and
// timeout are always enforced here.
func NewWooCommerceProvider(cfg config.WooCommerceConfig, httpClient *http.Client) (*WooCommerceProvider, error) {
	if !cfg.Enabled {
		return nil, apperr.New(apperr.InvalidInput, "woocommerce adapter is disabled")
	}
	key, err := commerce.ValidateProviderKey(cfg.ProviderKey)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	if cfg.ConsumerKey == "" || cfg.ConsumerSecret == "" {
		return nil, apperr.New(apperr.InvalidInput, "woocommerce credentials are required")
	}
	if cfg.Currency != config.WooCurrencyEGP && cfg.Currency != config.WooCurrencyUSD {
		return nil, apperr.New(apperr.InvalidInput, fmt.Sprintf("unsupported currency %q", cfg.Currency))
	}
	if cfg.DimensionUnit != config.WooDimensionCM {
		return nil, apperr.New(apperr.InvalidInput, fmt.Sprintf("unsupported dimension unit %q", cfg.DimensionUnit))
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = config.DefaultWooHTTPTimeout
	}
	return &WooCommerceProvider{
		key: key, currency: cfg.Currency, dimUnit: cfg.DimensionUnit,
		client: newClient(cfg.NormalizedBaseURL(), cfg.ConsumerKey, cfg.ConsumerSecret, timeout, httpClient),
	}, nil
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "woocommerce base URL is not a valid URL")
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return apperr.New(apperr.InvalidInput, "woocommerce base URL must use https")
	}
	if parsed.User != nil {
		return apperr.New(apperr.InvalidInput, "woocommerce base URL must not contain userinfo")
	}
	if parsed.RawQuery != "" {
		return apperr.New(apperr.InvalidInput, "woocommerce base URL must not contain query parameters")
	}
	if parsed.Fragment != "" {
		return apperr.New(apperr.InvalidInput, "woocommerce base URL must not contain a fragment")
	}
	return nil
}

// Key implements commerce.CommerceProvider: the configured generic
// instance key, never the string "woocommerce".
func (p *WooCommerceProvider) Key() commerce.ProviderKey { return p.key }

// UpsertProduct converges one Woo product to desired state:
//
//   - With an existing external ID: verify remote ownership by GET,
//     then PUT to current state (safe-zero stock included).
//   - Without one: SKU preflight lookup; empty → POST; exactly one
//     owned → recover by update; foreign or multiple → conflict.
//   - A duplicate-SKU POST error triggers one bounded ownership
//     recovery lookup before conflicting.
//
// Every metadata path writes safe-zero stock; the separate SetInventory
// restores availability afterwards.
func (p *WooCommerceProvider) UpsertProduct(ctx context.Context, req commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	if req.ProviderKey != p.key {
		return commerce.ProductUpsertResult{}, apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.OperationKey == "" {
		return commerce.ProductUpsertResult{}, apperr.New(apperr.InvalidInput, "product id and operation key are required")
	}
	// Phase 17 SKU ownership: a single-variant product publishes the
	// VARIANT's SKU as the remote SKU (catalog_products.sku is its
	// deprecated display mirror). Multi-variant parents keep the product
	// SKU; provider variations carry the variant SKUs.
	if len(req.Product.Variants) == 1 && req.Product.Variants[0].SKU != "" {
		req.Product.SKU = req.Product.Variants[0].SKU
	}
	payload, err := buildProductPayload(req, p.currency, p.dimUnit)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	var result commerce.ProductUpsertResult
	var err2 error
	if req.ExistingExternal != nil {
		result, err2 = p.updateMapped(ctx, req, payload)
	} else {
		result, err2 = p.createWithRecovery(ctx, req, payload)
	}
	if err2 != nil {
		return commerce.ProductUpsertResult{}, err2
	}
	// Phase 15 §84-§92: converge frame variations on the SAME remote
	// product (identity stable across simple<->framed, §116). Variation
	// ownership is metadata-proven; foreign variations are untouched.
	if len(req.Product.Configurations) > 0 {
		identities, err := p.syncWooVariations(ctx, result.ExternalProductID, req.Product)
		if err != nil {
			return commerce.ProductUpsertResult{}, err
		}
		result.Configurations = identities
	}
	return result, nil
}

// updateMapped verifies remote ownership of a mapped Woo ID, then PUTs
// current desired state. Missing or foreign remote products conflict;
// response ID mismatches conflict; inventory is never called here.
func (p *WooCommerceProvider) updateMapped(ctx context.Context, req commerce.ProductUpsertRequest, payload wooProductPayload) (commerce.ProductUpsertResult, error) {
	wooID, err := parseWooID(req.ExistingExternal.ExternalProductID)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	remote, err := p.getProduct(ctx, wooID)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if !ownershipMatches(remote.metadata(), req.ProductID, req.ProviderKey) {
		return commerce.ProductUpsertResult{}, commerce.ConflictError(
			fmt.Sprintf("woo product %d owned by another product or provider", wooID))
	}
	updated, err := p.putProduct(ctx, wooID, payload)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	if updated != wooID {
		return commerce.ProductUpsertResult{}, commerce.ConflictError(
			fmt.Sprintf("woo update identity mismatch: want %d got %d", wooID, updated))
	}
	return commerce.ProductUpsertResult{ExternalProductID: canonicalExternalID(updated)}, nil
}

// createWithRecovery runs SKU preflight: empty → POST; one owned →
// update and return; foreign/multiple → conflict.
func (p *WooCommerceProvider) createWithRecovery(ctx context.Context, req commerce.ProductUpsertRequest, payload wooProductPayload) (commerce.ProductUpsertResult, error) {
	found, err := p.lookupBySKU(ctx, req.Product.SKU)
	if err != nil {
		return commerce.ProductUpsertResult{}, err
	}
	switch len(found) {
	case 0:
		return p.postProduct(ctx, req, payload)
	case 1:
		if !ownershipMatches(found[0].metadata(), req.ProductID, req.ProviderKey) {
			return commerce.ProductUpsertResult{}, commerce.ConflictError(
				fmt.Sprintf("sku %q owned by another product or provider", req.Product.SKU))
		}
		wooID, err := wooResponseID(found[0])
		if err != nil {
			return commerce.ProductUpsertResult{}, err
		}
		updated, err := p.putProduct(ctx, wooID, payload)
		if err != nil {
			return commerce.ProductUpsertResult{}, err
		}
		if updated != wooID {
			return commerce.ProductUpsertResult{}, commerce.ConflictError("woo update identity mismatch")
		}
		return commerce.ProductUpsertResult{ExternalProductID: canonicalExternalID(updated)}, nil
	default:
		return commerce.ProductUpsertResult{}, commerce.ConflictError(
			fmt.Sprintf("multiple woo products share sku %q", req.Product.SKU))
	}
}

// postProduct creates via POST with duplicate-SKU recovery: one bounded
// ownership lookup decides between recovering our product and conflict.
func (p *WooCommerceProvider) postProduct(ctx context.Context, req commerce.ProductUpsertRequest, payload wooProductPayload) (commerce.ProductUpsertResult, error) {
	var created wooProductResponse
	_, err := p.client.do(ctx, http.MethodPost, "/products", nil, payload, &created)
	if err == nil {
		wooID, idErr := wooResponseID(created)
		if idErr != nil {
			// The remote operation may have occurred: Temporary so the
			// caller retries into SKU ownership recovery.
			return commerce.ProductUpsertResult{}, commerce.TemporaryError("woo create response missing product id")
		}
		return commerce.ProductUpsertResult{ExternalProductID: canonicalExternalID(wooID)}, nil
	}
	if isDuplicateSKU(err) {
		found, lookupErr := p.lookupBySKU(ctx, req.Product.SKU)
		if lookupErr != nil {
			return commerce.ProductUpsertResult{}, lookupErr
		}
		if len(found) == 1 && ownershipMatches(found[0].metadata(), req.ProductID, req.ProviderKey) {
			wooID, idErr := wooResponseID(found[0])
			if idErr != nil {
				return commerce.ProductUpsertResult{}, commerce.TemporaryError("woo recovery response missing product id")
			}
			updated, putErr := p.putProduct(ctx, wooID, payload)
			if putErr != nil {
				return commerce.ProductUpsertResult{}, putErr
			}
			if updated != wooID {
				return commerce.ProductUpsertResult{}, commerce.ConflictError("woo update identity mismatch")
			}
			return commerce.ProductUpsertResult{ExternalProductID: canonicalExternalID(updated)}, nil
		}
		return commerce.ProductUpsertResult{}, commerce.ConflictError(
			fmt.Sprintf("sku %q conflicts with an existing woo product", req.Product.SKU))
	}
	return commerce.ProductUpsertResult{}, err
}

// SetInventory sets remote availability with a narrow payload: stock
// fields only, never product metadata. Not-ready forces zero.
func (p *WooCommerceProvider) SetInventory(ctx context.Context, req commerce.InventoryUpdateRequest) error {
	if req.ProviderKey != p.key {
		return apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" {
		return apperr.New(apperr.InvalidInput, "product id is required")
	}
	wooID, err := parseWooID(req.ExternalProductID)
	if err != nil {
		return err
	}
	payload, err := buildInventoryPayload(req)
	if err != nil {
		return err
	}
	var updated wooProductResponse
	_, err = p.client.do(ctx, http.MethodPut, "/products/"+strconv.FormatInt(wooID, 10), nil, payload, &updated)
	if err != nil {
		return err
	}
	// Success must prove which product was acknowledged: missing or
	// malformed identities are Temporary (the remote may have applied
	// stock, but the response names no product), and a different valid
	// identity is a Conflict. Retrying the same inventory update is
	// idempotent, so no rollback is attempted.
	id, err := wooResponseID(updated)
	if err != nil {
		return err
	}
	if id != wooID {
		return commerce.ConflictError(
			fmt.Sprintf("woo inventory identity mismatch: want %d got %d", wooID, id))
	}
	return nil
}

// lookupBySKU returns Woo products with an exact SKU match, bounded.
func (p *WooCommerceProvider) lookupBySKU(ctx context.Context, sku string) ([]wooProductResponse, error) {
	query := url.Values{}
	query.Set("sku", sku)
	query.Set("per_page", "10")
	var found []wooProductResponse
	if _, err := p.client.do(ctx, http.MethodGet, "/products", query, nil, &found); err != nil {
		return nil, err
	}
	if found == nil {
		return []wooProductResponse{}, nil
	}
	return found, nil
}

// getProduct retrieves one Woo product with ownership metadata.
func (p *WooCommerceProvider) getProduct(ctx context.Context, wooID int64) (wooProductResponse, error) {
	var remote wooProductResponse
	_, err := p.client.do(ctx, http.MethodGet, "/products/"+strconv.FormatInt(wooID, 10), nil, nil, &remote)
	if err != nil {
		return wooProductResponse{}, err
	}
	return remote, nil
}

// putProduct writes current desired state (safe-zero stock included).
func (p *WooCommerceProvider) putProduct(ctx context.Context, wooID int64, payload wooProductPayload) (int64, error) {
	var updated wooProductResponse
	_, err := p.client.do(ctx, http.MethodPut, "/products/"+strconv.FormatInt(wooID, 10), nil, payload, &updated)
	if err != nil {
		return 0, err
	}
	return wooResponseID(updated)
}

// wooResponseID extracts and validates the numeric Woo identity.
func wooResponseID(response wooProductResponse) (int64, error) {
	id, err := parseWooID(response.ID.String())
	if err != nil {
		return 0, commerce.TemporaryError("woo response missing product id")
	}
	return id, nil
}

// isDuplicateSKU reports Woo duplicate-SKU conflict semantics for the
// bounded recovery path.
func isDuplicateSKU(err error) bool {
	var providerErr *commerce.ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Kind == commerce.ErrorConflict &&
			strings.Contains(strings.ToLower(providerErr.Error()), wooCodeDuplicateSKU)
	}
	return false
}
