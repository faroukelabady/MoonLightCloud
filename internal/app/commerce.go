package app

import (
	"fmt"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	shopifyadapter "github.com/faroukelabady/MoonLightCloud/internal/commerce/shopify"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/woocommerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"log/slog"
)

// NewCommerceRegistry builds the provider registry from runtime
// configuration. Disabled providers register nothing; each enabled
// provider registers exactly one logical instance under its configured
// key. Construction performs no network I/O, so Cloud starts even when
// any provider is unreachable.
func NewCommerceRegistry(cfg config.Config, coordinator commerce.ProductSyncCoordinator) (*commerce.Registry, error) {
	if cfg.Shopify.Enabled && coordinator == nil {
		return nil, fmt.Errorf("Shopify construction requires the durable product-sync coordinator")
	}
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
		provider, err := shopifyadapter.NewShopifyProvider(cfg.Shopify, nil, coordinator)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(provider.Key(), provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// NewCommerceService wires orchestration over live catalog reads, the
// durable mapping repository and the stored image files (ADR-0056).
func NewCommerceService(mappings commerce.ProductMappingRepository, catalogRepo catalog.Repository, media commerce.MediaContentReader, registry *commerce.Registry, log *slog.Logger) *commerce.CommerceService {
	service := commerce.NewCommerceService(registry, mappings,
		commerce.NewCatalogCommerceSource(catalog.NewService(catalogRepo)), log)
	service.SetMediaContent(media)
	return service
}
