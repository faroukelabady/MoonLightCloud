package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	shopifyadapter "github.com/faroukelabady/MoonLightCloud/internal/commerce/shopify"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/woocommerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

// newCommerceRegistry builds the provider registry from runtime
// configuration. Disabled providers register nothing; each enabled
// provider registers exactly one logical instance under its configured
// key. Construction performs no network I/O, so Cloud starts even when
// any provider is unreachable.
func newCommerceRegistry(cfg config.Config) (*commerce.Registry, error) {
	registry := commerce.NewRegistry()
	if cfg.WooCommerce.Enabled {
		provider, err := woocommerce.NewWooCommerceProvider(cfg.WooCommerce, nil)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(provider.Key(), provider); err != nil {
			return nil, err
		}
	}
	if cfg.Shopify.Enabled {
		provider, err := shopifyadapter.NewShopifyProvider(cfg.Shopify, nil)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(provider.Key(), provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// newCommerceService wires orchestration over live catalog reads and the
// durable mapping repository.
func newCommerceService(store postgres.Devices, registry *commerce.Registry, log *slog.Logger) *commerce.CommerceService {
	return commerce.NewCommerceService(registry, store,
		commerce.NewCatalogCommerceSource(catalog.NewService(store)), log)
}

func asCommerceProviderError(err error) (*commerce.ProviderError, bool) {
	var providerErr *commerce.ProviderError
	if errors.As(err, &providerErr) {
		return providerErr, true
	}
	return nil, false
}

// runCommerceSync executes one synchronous product sync and prints the
// bounded safe result. Separated from CLI wiring for testability.
func runCommerceSync(ctx context.Context, service *commerce.CommerceService, out io.Writer, providerKey, productID string) error {
	result, err := service.SyncProduct(ctx, providerKey, productID)
	if err != nil {
		if providerErr, ok := asCommerceProviderError(err); ok {
			fmt.Fprintf(out, "provider=%s product=%s error_kind=%s retryable=%v error=%s\n",
				providerKey, productID, providerErr.Kind, providerErr.Retryable(), providerErr.Message)
		} else {
			fmt.Fprintf(out, "provider=%s product=%s error=%s\n", providerKey, productID, err.Error())
		}
		return err
	}
	fmt.Fprintf(out, "provider=%s product=%s outcome=%s external=%s mapping_created=%v inventory_updated=%v product_key=%s inventory_key=%s\n",
		result.ProviderKey, productID, result.Outcome, result.ExternalProductID,
		result.MappingCreated, result.InventoryUpdated,
		result.ProductOperationKey, result.InventoryOperationKey)
	return nil
}
