// Package woocommerce implements the first concrete CommerceProvider:
// WooCommerce REST API v3 over HTTPS with HTTP Basic Auth. Cloud-only
// outbound integration: MoonLight stays authoritative, Woo receives
// desired product state and safe availability. Simple products only;
// no taxonomy, images, orders, webhooks, or jobs. See ADR-0032 and
// docs/operations/woocommerce.md.
package woocommerce

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// MoonLight ownership metadata keys. Permanent ownership is
// ProductID + ProviderKey; the operation key is desired-state identity
// that legitimately rotates with revisions.
const (
	metaProductID    = "_moonlight_product_id"
	metaProviderKey  = "_moonlight_provider_key"
	metaOperationKey = "_moonlight_product_operation_key"
	metaCatalogRev   = "_moonlight_catalog_revision"
	metaPolicyRev    = "_moonlight_policy_revision"
)

// Woo wire field values (verified against WooCommerce REST API v3 docs).
const (
	wooTypeSimple        = "simple"
	wooStatusPublish     = "publish"
	wooStatusDraft       = "draft"
	wooVisibilityVisible = "visible"
	wooVisibilityHidden  = "hidden"
	wooStockInStock      = "instock"
	wooStockOutOfStock   = "outofstock"
	wooBackordersNo      = "no"

	// Woo duplicate-SKU error code: recovery signal, not blind failure.
	wooCodeDuplicateSKU = "product_invalid_sku"
)

type wooMetaDatum struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type wooDimensions struct {
	Length string `json:"length,omitempty"`
	Width  string `json:"width,omitempty"`
	Height string `json:"height,omitempty"`
}

// wooProductPayload is the MoonLight-owned subset of a Woo product write.
// Unmanaged fields (categories, tags, images, tax/shipping classes,
// featured, reviews, plugin metadata) are always omitted so Woo PUT
// merges without clearing manually managed state.
type wooProductPayload struct {
	Name              string         `json:"name"`
	Type              string         `json:"type"`
	Status            string         `json:"status"`
	CatalogVisibility string         `json:"catalog_visibility"`
	Description       string         `json:"description"`
	SKU               string         `json:"sku"`
	RegularPrice      string         `json:"regular_price"`
	ManageStock       bool           `json:"manage_stock"`
	StockQuantity     int64          `json:"stock_quantity"`
	StockStatus       string         `json:"stock_status"`
	Backorders        string         `json:"backorders"`
	Dimensions        *wooDimensions `json:"dimensions,omitempty"`
	MetaData          []wooMetaDatum `json:"meta_data,omitempty"`
}

// wooInventoryPayload is the narrow inventory-only update: no product
// metadata fields, keeping the operation idempotent and minimal.
type wooInventoryPayload struct {
	ManageStock   bool   `json:"manage_stock"`
	StockQuantity int64  `json:"stock_quantity"`
	StockStatus   string `json:"stock_status"`
	Backorders    string `json:"backorders"`
}

// wooProductResponse decodes the fields the adapter trusts: the numeric
// identity and ownership metadata. All other response fields are ignored.
type wooProductResponse struct {
	ID       json.Number    `json:"id"`
	MetaData []wooMetaDatum `json:"meta_data"`
}

// wooErrorResponse mirrors Woo's standard error shape.
type wooErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Status int `json:"status"`
	} `json:"data"`
}

func (r wooProductResponse) metadata() map[string]string {
	out := map[string]string{}
	for _, item := range r.MetaData {
		out[item.Key] = item.Value
	}
	return out
}

// minorToDecimal converts exact int64 minor units to a Woo decimal price
// string with integer arithmetic only. No floats anywhere near money.
func minorToDecimal(minor int64) (string, error) {
	if minor < 0 {
		return "", commerce.ValidationError("negative price cannot be represented")
	}
	return fmt.Sprintf("%d.%02d", minor/100, minor%100), nil
}

// selectName returns the Arabic-primary product name with English
// fallback. Empty names are a validation failure, never silent.
func selectName(product commerce.CommerceProduct) (string, error) {
	var english string
	for _, name := range product.Names {
		switch strings.ToLower(name.Locale) {
		case "ar":
			if strings.TrimSpace(name.Name) != "" {
				return name.Name, nil
			}
		case "en":
			if english == "" && strings.TrimSpace(name.Name) != "" {
				english = name.Name
			}
		}
	}
	if english != "" {
		return english, nil
	}
	return "", commerce.ValidationError("product has no Arabic or English name")
}

// selectDescription returns the Arabic-primary description with English
// fallback. Empty is allowed when neither exists.
func selectDescription(product commerce.CommerceProduct) string {
	var english string
	for _, name := range product.Names {
		description := ""
		if product.Descriptions != nil {
			description = product.Descriptions[name.Locale]
		}
		switch strings.ToLower(name.Locale) {
		case "ar":
			if strings.TrimSpace(description) != "" {
				return description
			}
		case "en":
			if english == "" && strings.TrimSpace(description) != "" {
				english = description
			}
		}
	}
	return english
}

// selectPrice returns the exact decimal regular price for the configured
// currency. No silent cross-currency fallback: a missing configured
// price is a validation failure.
func selectPrice(product commerce.CommerceProduct, currency string) (string, error) {
	for _, price := range product.Prices {
		if strings.EqualFold(price.Currency, currency) {
			return minorToDecimal(price.AmountMinor)
		}
	}
	return "", commerce.ValidationError(fmt.Sprintf("product has no %s price", currency))
}

// buildProductPayload maps the frozen provider-neutral DTO into the Woo
// write payload with safe-zero stock: every create/update leaves Woo at
// 0/out-of-stock until the separate SetInventory restores availability.
func buildProductPayload(req commerce.ProductUpsertRequest, currency, dimensionUnit string) (wooProductPayload, error) {
	product := req.Product
	if strings.TrimSpace(product.SKU) == "" {
		return wooProductPayload{}, commerce.ValidationError("product SKU must not be empty")
	}
	name, err := selectName(product)
	if err != nil {
		return wooProductPayload{}, err
	}
	price, err := selectPrice(product, currency)
	if err != nil {
		return wooProductPayload{}, err
	}
	status, visibility := wooStatusDraft, wooVisibilityHidden
	if req.Published {
		status, visibility = wooStatusPublish, wooVisibilityVisible
	}
	payload := wooProductPayload{
		Name: name, Type: wooTypeSimple, Status: status, CatalogVisibility: visibility,
		Description: selectDescription(product), SKU: product.SKU, RegularPrice: price,
		ManageStock: true, StockQuantity: 0, StockStatus: wooStockOutOfStock, Backorders: wooBackordersNo,
		MetaData: []wooMetaDatum{
			{Key: metaProductID, Value: req.ProductID},
			{Key: metaProviderKey, Value: string(req.ProviderKey)},
			{Key: metaOperationKey, Value: req.OperationKey},
			{Key: metaCatalogRev, Value: strconv.FormatInt(req.CatalogRevision, 10)},
			{Key: metaPolicyRev, Value: strconv.FormatInt(req.PolicyRevision, 10)},
		},
	}
	if dimensionUnit == "cm" && (product.WidthCM != nil || product.HeightCM != nil) {
		dimensions := &wooDimensions{}
		if product.WidthCM != nil {
			dimensions.Width = strconv.Itoa(*product.WidthCM)
		}
		if product.HeightCM != nil {
			dimensions.Height = strconv.Itoa(*product.HeightCM)
		}
		payload.Dimensions = dimensions
	}
	return payload, nil
}

// buildInventoryPayload maps one inventory request into the narrow Woo
// update. Not-ready forces zero even if the caller passed a quantity.
func buildInventoryPayload(req commerce.InventoryUpdateRequest) (wooInventoryPayload, error) {
	if req.AvailableQuantity < 0 || req.AvailableQuantity > maxWooQuantity {
		return wooInventoryPayload{}, commerce.ValidationError("available quantity out of range")
	}
	quantity := req.AvailableQuantity
	if !req.Ready {
		quantity = 0
	}
	status := wooStockOutOfStock
	if quantity > 0 {
		status = wooStockInStock
	}
	return wooInventoryPayload{
		ManageStock: true, StockQuantity: quantity, StockStatus: status, Backorders: wooBackordersNo,
	}, nil
}

// maxWooQuantity is the frozen Phase 5C stock bound mirrored here.
const maxWooQuantity = 2147483647

// ownershipMatches requires BOTH MoonLight product ID and provider key in
// remote metadata. SKU alone never proves ownership.
func ownershipMatches(meta map[string]string, productID string, key commerce.ProviderKey) bool {
	return meta[metaProductID] == productID && meta[metaProviderKey] == string(key)
}

// parseWooID normalizes a Woo numeric identity to canonical decimal and
// validates it as a positive mappable ID. Leading zeros normalize
// ("000794" → 794); zero, negatives, and non-numerics are rejected.
func parseWooID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.New(apperr.InvalidInput, fmt.Sprintf("invalid Woo product id %q", raw))
	}
	return id, nil
}

// canonicalExternalID formats a Woo numeric ID for the generic mapping.
func canonicalExternalID(id int64) string {
	return strconv.FormatInt(id, 10)
}
