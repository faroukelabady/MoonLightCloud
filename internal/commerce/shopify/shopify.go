// Package shopify is the second concrete CommerceProvider (Phase 11):
// Shopify GraphQL Admin API over HTTPS. It reuses the frozen
// provider-neutral commerce boundary (CommerceProvider), the frozen
// CommerceOrderProvider capability, durable commerce_product_mappings,
// Phase 5C/9 derived availability, and the Phase 6C order
// reconciliation/webhook architecture unchanged. Provider-specific
// GraphQL lives only here.
//
// Core invariant (ADR-0044): MoonLight owns business identity and
// inventory truth; Shopify owns only provider state. Shopify SKU never
// owns MoonLight product identity, Shopify inventory never owns Retail
// stock, Shopify orders never become Retail Sales, and Shopify payloads
// never choose the MoonLight Store.
package shopify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

// Ownership metafields: durable MoonLight remote ownership under the
// dedicated "moonlight" namespace. Permanent ownership is
// MoonLight ProductID + ProviderKey; the operation key and revisions are
// refresh metadata and never identity. No credentials, secrets,
// customer data, or financial history ever live here.
const (
	metafieldNamespace = "moonlight"

	metafieldProductID        = "product_id"
	metafieldProviderKey      = "provider_key"
	metafieldProductOperation = "product_operation_key"
	metafieldCatalogRevision  = "catalog_revision"
	metafieldPolicyRevision   = "policy_revision"
	metafieldManagedVariantID = "managed_variant_id"
)

// ShopifyProvider implements commerce.CommerceProvider (and the
// orders.CommerceOrderProvider capability) for one configured Shopify
// shop instance. Construction performs no network I/O: Cloud starts even
// when Shopify is unreachable.
type ShopifyProvider struct {
	key           commerce.ProviderKey
	currency      string
	locationGID   string
	publicationID string
	client        *Client
	coordinator   commerce.ProductSyncCoordinator

	// lazyCurrency guards the one-time shop currency verification (§37):
	// performed before the first price-changing product operation, never
	// at startup.
	currencyMu       sync.Mutex
	currencyVerified bool
}

// NewShopifyProvider validates configuration eagerly (no deferred
// failures at first sync) and builds the bounded transport. The injected
// *http.Client may carry a test TLS transport; redirect policy and
// timeout are always enforced inside the client.
func NewShopifyProvider(cfg config.ShopifyConfig, httpClient *http.Client, coordinators ...commerce.ProductSyncCoordinator) (*ShopifyProvider, error) {
	if !cfg.Enabled {
		return nil, apperr.New(apperr.InvalidInput, "shopify adapter is disabled")
	}
	key, err := commerce.ValidateProviderKey(cfg.ProviderKey)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := validateShopDomain(cfg.ShopDomain); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "shopify shop domain must be a canonical <shop>.myshopify.com domain")
	}
	if err := validateAPIVersion(cfg.APIVersion); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "shopify API version must be a pinned YYYY-MM stable version")
	}
	if cfg.AccessToken == "" {
		return nil, apperr.New(apperr.InvalidInput, "shopify access token is required")
	}
	if cfg.Currency != config.ShopifyCurrencyEGP && cfg.Currency != config.ShopifyCurrencyUSD {
		return nil, apperr.New(apperr.InvalidInput, fmt.Sprintf("unsupported currency %q", cfg.Currency))
	}
	if _, err := ParseGID(cfg.LocationID, ResourceLocation); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "shopify location must be gid://shopify/Location/<id>")
	}
	if _, err := ParseGID(cfg.PublicationID, ResourcePublication); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "shopify publication must be gid://shopify/Publication/<id>")
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = config.DefaultShopifyHTTPTimeout
	}
	endpoint := "https://" + cfg.ShopDomain + "/admin/api/" + cfg.APIVersion + "/graphql.json"
	var coordinator commerce.ProductSyncCoordinator
	if len(coordinators) > 0 {
		coordinator = coordinators[0]
	}
	return &ShopifyProvider{
		coordinator: coordinator,
		key:         key, currency: cfg.Currency,
		locationGID: cfg.LocationID, publicationID: cfg.PublicationID,
		client: newClient(endpoint, cfg.AccessToken, cfg.ClientSecret, timeout, httpClient),
	}, nil
}

// validateShopDomain accepts only a canonical Shopify Admin shop domain:
// a lowercase host under myshopify.com. Scheme, userinfo, port, path,
// query, fragment, whitespace, and control characters are all refused;
// the GraphQL endpoint is constructed internally from this value.
func validateShopDomain(domain string) error {
	if domain == "" || domain != strings.ToLower(domain) {
		return errInvalidDomain
	}
	if len(domain) > 253 {
		return errInvalidDomain
	}
	for _, r := range domain {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return errInvalidDomain
		}
	}
	if strings.Contains(domain, "..") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return errInvalidDomain
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 3 {
		return errInvalidDomain
	}
	if labels[len(labels)-2] != "myshopify" || labels[len(labels)-1] != "com" {
		return errInvalidDomain
	}
	for _, label := range labels {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errInvalidDomain
		}
	}
	return nil
}

// validateAPIVersion requires an explicit date-based version handle
// (YYYY-MM, year >= 2024). "latest" and "unstable" are refused: the
// production version is pinned deliberately.
func validateAPIVersion(version string) error {
	if version != config.SupportedShopifyAPIVersion {
		return errInvalidVersion
	}
	return nil
}

// Key implements commerce.CommerceProvider: the configured generic
// instance key (for example "shopify-main"), never the string "shopify".
func (p *ShopifyProvider) Key() commerce.ProviderKey { return p.key }

// endpoint returns the constructed Admin GraphQL endpoint (diagnostics
// only; never carries credentials).
func (p *ShopifyProvider) endpoint() string { return p.client.endpoint }

// remoteFence records catalog/policy revisions and logical operation identity.
// It rejects stale starts and detects visible supersession; it does not
// atomically fence subsequent unconditional Shopify Product mutations.
type remoteFence struct {
	catalogRevision int64
	policyRevision  int64
	operationKey    string
}

// fenceFrom parses the fence from ownership metafield values. Missing
// revision keys mean no fence (legacy/foreign-created state): the
// operation proceeds and stamps its own fence.
func fenceFrom(values map[string]string) (remoteFence, error) {
	fence := remoteFence{operationKey: values[metafieldProductOperation]}
	catalog, err := parseRevision(values[metafieldCatalogRevision])
	if err != nil {
		return remoteFence{}, err
	}
	policy, err := parseRevision(values[metafieldPolicyRevision])
	if err != nil {
		return remoteFence{}, err
	}
	fence.catalogRevision = catalog
	fence.policyRevision = policy
	return fence, nil
}

// parseRevision parses one fence revision; empty means "no fence" (0).
func parseRevision(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, commerce.ConflictError("shopify ownership revision metadata is malformed")
	}
	return parsed, nil
}

// supersedes reports whether the remote fence proves a newer desired
// state than the caller's revisions: the caller is stale.
func (f remoteFence) supersedes(catalogRevision, policyRevision int64) bool {
	return f.catalogRevision > catalogRevision || f.policyRevision > policyRevision
}

// supersededBy reports whether the fence names a different completed
// operation than ours. Operation keys are deterministic in the desired
// state, so a different key always means a genuinely different (newer)
// operation wrote after us.
func (f remoteFence) supersededBy(operationKey string) bool {
	return f.operationKey != "" && f.operationKey != operationKey
}

// errSuperseded is the retryable failure returned when a newer operation
// owns the remote product. The caller's retry re-reads fresh desired
// state and reconverges; no stale write ever completes with success.
func errSuperseded() error {
	return commerce.TemporaryError("shopify product superseded by a newer operation")
}

// ensureShopCurrency verifies the shop's currency against the
// configured provider currency exactly once per process, lazily before
// the first price-changing operation. A mismatch blocks every product
// mutation: EGP minor units are never published into a USD shop.
func (p *ShopifyProvider) ensureShopCurrency(ctx context.Context) error {
	p.currencyMu.Lock()
	defer p.currencyMu.Unlock()
	if p.currencyVerified {
		return nil
	}
	var out shopCurrencyResponse
	if err := p.client.do(ctx, docShopCurrency, nil, &out); err != nil {
		return err
	}
	shopCurrency := strings.ToUpper(strings.TrimSpace(out.Shop.CurrencyCode))
	if shopCurrency == "" {
		return commerce.TemporaryError("shop currency unavailable")
	}
	if shopCurrency != p.currency {
		return commerce.ValidationError(fmt.Sprintf(
			"shopify shop currency %s does not match configured currency %s", shopCurrency, p.currency))
	}
	p.currencyVerified = true
	return nil
}

// ownershipMetafields builds the targeted moonlight-namespace ownership
// writes. Nothing outside this namespace is ever written.
func ownershipMetafields(productGID string, req commerce.ProductUpsertRequest, managedVariantGID string) []map[string]any {
	entry := func(key, value string) map[string]any {
		return map[string]any{
			"ownerId":   productGID,
			"namespace": metafieldNamespace,
			"key":       key,
			"type":      "single_line_text_field",
			"value":     value,
		}
	}
	fields := []map[string]any{
		entry(metafieldProductID, req.ProductID),
		entry(metafieldProviderKey, string(req.ProviderKey)),
		entry(metafieldProductOperation, req.OperationKey),
		entry(metafieldCatalogRevision, fmt.Sprintf("%d", req.CatalogRevision)),
		entry(metafieldPolicyRevision, fmt.Sprintf("%d", req.PolicyRevision)),
	}
	if managedVariantGID != "" {
		fields = append(fields, entry(metafieldManagedVariantID, managedVariantGID))
	}
	return fields
}

// ownership reads ownership metafields into a map.
func ownership(fields []gqlMetafield) map[string]string {
	values := map[string]string{}
	for _, field := range fields {
		if field.Namespace == metafieldNamespace {
			values[field.Key] = field.Value
		}
	}
	return values
}

// ownershipMatches reports whether remote ownership metadata proves the
// exact MoonLight product identity AND provider instance. SKU alone
// never proves ownership.
func ownershipMatches(values map[string]string, productID string, providerKey commerce.ProviderKey) bool {
	return values[metafieldProductID] == productID &&
		values[metafieldProviderKey] == string(providerKey) &&
		productID != "" && providerKey != ""
}

// failUserErrors converts mutation userErrors into classified errors:
// remote validation feedback is terminal for the same operation key and
// is always scrubbed and bounded before it can surface.
func (p *ShopifyProvider) failUserErrors(operation string, errs []userError) error {
	if len(errs) == 0 {
		return nil
	}
	first := errs[0]
	code := boundField(p.client.scrub.scrub(first.Code), codeLimit)
	message := boundField(p.client.scrub.scrub(first.Message), messageLimit)
	if code == "" && message == "" {
		message = "user error"
	}
	return commerce.ValidationError(p.client.safeMessage(fmt.Sprintf("shopify %s user error %s: %s", operation, code, message)))
}

// idempotencyKey derives the deterministic Shopify idempotency key for
// one logical operation from the frozen MoonLight operation identity.
// Same operation retry yields the same key; changed desired state yields
// a different key.
func idempotencyKey(step, operationKey string) string {
	sum := sha256Hex(step + "|" + operationKey)
	return "moonlight-" + sum[:32]
}

// managedVariant resolves the exact MoonLight-managed variant from a
// loaded product: the recorded managed variant identity must match and
// the exact-SKU variant must be unique. Anything else fails closed.
func managedVariant(product *gqlProduct, sku string, recordedVariantID string) (gqlVariant, error) {
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
	if recordedVariantID != "" {
		variantID, err := ParseGID(exact[0].ID, ResourceProductVariant)
		if err != nil {
			return gqlVariant{}, commerce.ConflictError("shopify managed variant identity malformed")
		}
		if variantID != recordedVariantID {
			return gqlVariant{}, commerce.ConflictError("shopify managed variant identity mismatch")
		}
	}
	return exact[0], nil
}

// parseVariantGID validates and canonicalizes a variant GID.
func parseVariantGID(raw string) (string, error) {
	return ParseGID(raw, ResourceProductVariant)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
