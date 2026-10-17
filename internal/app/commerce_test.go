package app

import (
	"context"
	"errors"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

type refusingCommerceCoordinator struct {
	calls int
	err   error
}

func (c *refusingCommerceCoordinator) WithProductSync(context.Context, commerce.ProviderKey, string, func(context.Context) error) error {
	c.calls++
	return c.err
}

func TestSharedRegistryRequiresAndUsesShopifyCoordinator(t *testing.T) {
	cfg := config.Config{Shopify: config.ShopifyConfig{
		Enabled: true, ProviderKey: "test-shopify", ShopDomain: "test-shop.myshopify.com",
		APIVersion: config.SupportedShopifyAPIVersion, AccessToken: "synthetic-construction-test",
		Currency: config.ShopifyCurrencyEGP, LocationID: "gid://shopify/Location/1",
		PublicationID: "gid://shopify/Publication/2",
	}}
	if _, err := NewCommerceRegistry(cfg, nil); err == nil {
		t.Fatal("Shopify must not be constructed without the durable coordinator")
	}
	want := errors.New("coordinator refused before network")
	coordinator := &refusingCommerceCoordinator{err: want}
	registry, err := NewCommerceRegistry(cfg, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.calls != 0 {
		t.Fatal("construction performed business work")
	}
	provider, err := registry.Get("test-shopify")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.UpsertProduct(context.Background(), commerce.ProductUpsertRequest{
		ProviderKey: "test-shopify", ProductID: "11111111-1111-4111-8111-111111111111",
	})
	if !errors.Is(err, want) || coordinator.calls != 1 {
		t.Fatalf("coordinator dependency was lost: calls=%d err=%v", coordinator.calls, err)
	}
}
