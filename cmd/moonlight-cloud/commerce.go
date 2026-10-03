package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/google/uuid"
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

// This is an explicit local operator action, never an automatic retry path.
// Current remote state or elapsed time is not evidence of request settlement.
func commerceMutationBarrierCmd(args []string, stdout, stderr io.Writer, resolve bool) error {
	name := "commerce product-sync-status"
	if resolve {
		name = "commerce resolve-product-sync"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "", "provider key")
	product := fs.String("product", "", "MoonLight product UUID")
	var operation, resolution *string
	var confirmed *bool
	if resolve {
		operation = fs.String("operation", "", "exact blocked operation UUID")
		resolution = fs.String("resolution", "", "remote_completed or remote_not_applied")
		confirmed = fs.Bool("confirm-remote-settled", false, "operator independently verified this remote request has finished or was never applied; current state or elapsed time is insufficient")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	key, err := commerce.ValidateProviderKey(*provider)
	if err != nil {
		return fmt.Errorf("invalid provider key")
	}
	productUUID, err := uuid.Parse(*product)
	if err != nil {
		return fmt.Errorf("product_id must be a UUID")
	}
	if resolve {
		if _, err := uuid.Parse(*operation); err != nil {
			return fmt.Errorf("operation_id must be a UUID")
		}
		if !*confirmed || (*resolution != "remote_completed" && *resolution != "remote_not_applied") {
			return fmt.Errorf("explicit remote settlement confirmation required")
		}
	}
	env, err := openCommerceEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	ctx := context.Background()
	if resolve {
		if err := env.store.ResolveProductMutationBarrier(ctx, key, productUUID.String(), *operation, *resolution, *confirmed); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "provider=%s product=%s operation=%s resolution=%s recorded; run sync-product explicitly to re-read canonical desired state\n", key, productUUID.String(), *operation, *resolution)
		return nil
	}
	status, err := env.store.GetProductMutationBarrier(ctx, key, productUUID.String())
	if err != nil {
		return err
	}
	if status.OperationID == "" {
		fmt.Fprintf(stdout, "provider=%s product=%s barrier=none\n", key, productUUID.String())
		return nil
	}
	fmt.Fprintf(stdout, "provider=%s product=%s operation=%s state=%s code=COMMERCE_MUTATION_UNCERTAIN\n", key, productUUID.String(), status.OperationID, status.State)
	return nil
}
