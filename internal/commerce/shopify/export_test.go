package shopify

import (
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"testing"
)

// External-package integration tests use the same strict stateful TLS fixture
// without exposing test-only controls in the production adapter API.
type R3BundleFixture struct{ fixture *bundleFixture }

func NewR3BundleFixture(t *testing.T) *R3BundleFixture { return &R3BundleFixture{newBundleFixture(t)} }
func (f *R3BundleFixture) Provider(t *testing.T, coordinator commerce.ProductSyncCoordinator) *ShopifyProvider {
	t.Helper()
	p, err := NewShopifyProvider(testConfig(), harnessClient(t, f.fixture.h), coordinator)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f *R3BundleFixture) Creates() int { return f.fixture.creates }
func (f *R3BundleFixture) ChoiceCount(external string) int {
	return len(f.fixture.resources[FormatGID(ResourceProduct, external)]["variants"].(map[string]any)["nodes"].([]any))
}
