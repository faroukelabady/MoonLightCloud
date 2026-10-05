package app

import (
	"context"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
)

// catalogAdminDevices adapts existing Store bindings and the device
// lifecycle to catalog-admin delivery gating. No new authority is
// introduced: bindings stay immutable, revocation stays owned by auth.
type catalogAdminDevices struct {
	stores interface {
		CatalogAdminBindingStore(ctx context.Context, deviceID string) (string, error)
	}
	auth auth.Service
}

// CatalogAdminBindingStore resolves the current Store binding. It is
// implemented on postgres.Devices; declared here so app wiring stays
// against the narrow capability.
func (c catalogAdminDevices) BindingStore(ctx context.Context, deviceID string) (string, error) {
	return c.stores.CatalogAdminBindingStore(ctx, deviceID)
}

func (c catalogAdminDevices) DeviceActive(ctx context.Context, deviceID string) (bool, error) {
	dev, err := c.auth.Get(ctx, deviceID)
	if err != nil {
		return false, err
	}
	return dev.Status == auth.StatusActive, nil
}

func (c catalogAdminDevices) DeviceName(ctx context.Context, deviceID string) string {
	dev, err := c.auth.Get(ctx, deviceID)
	if err != nil {
		return ""
	}
	return dev.Name
}

var _ = postgres.Devices{}
