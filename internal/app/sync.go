package app

import (
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// One local contract snapshot per composed application. The diagnostic event
// remains explicit; creating another App cannot change this one's capabilities.
func syncEventRegistry() sync.Registry {
	return sync.NewRegistry(map[string]sync.Validator{
		sync.DiagnosticEventType:                       nil,
		sale.EventSaleFinalizedV3:                      ValidateSaleV3Payload,
		returnrefund.EventReturnRefundFinalizedV1:      ValidateReturnRefundPayload,
		catalog.EventCategorySnapshotV2:                ValidateCatalogCategoryV2Payload,
		catalog.EventTagSnapshotV1:                     ValidateCatalogTagPayload,
		catalog.EventProductSnapshotV2:                 ValidateCatalogProductV2Payload,
		catalog.EventProductSalesPolicySnapshotV1:      ValidateCatalogProductSalesPolicyPayload,
		catalog.EventProductConfigurationSnapshotV1:    ValidateCatalogProductConfigurationsPayload,
		catalog.EventProductVariantSnapshotV1:          ValidateCatalogProductVariantPayload,
		catalog.EventProductTypeSnapshotV1:             ValidateCatalogProductTypePayload,
		catalog.EventInventoryProductVariantSnapshotV1: ValidateCatalogProductVariantInventoryPayload,
	})
}
