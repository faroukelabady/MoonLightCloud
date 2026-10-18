package app

import (
	"context"

	adapterhttp "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
)

// catalogAdminBindingReader exposes immutable Store binding reads.
type catalogAdminBindingReader interface {
	CatalogAdminBindingStore(ctx context.Context, deviceID string) (string, error)
}

// catalogAdminDevices combines Store bindings with the auth-owned device lifecycle.
type catalogAdminDevices struct {
	stores catalogAdminBindingReader
	auth   auth.Service
}

// CatalogAdminBindingStore resolves the current Store binding. It is
// implemented on postgres.StoreRegistry; declared here so app wiring stays
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

var (
	_ catalogAdminBindingReader  = postgres.StoreRegistry{}
	_ adapterhttp.StoreRegistrar = postgres.StoreRegistry{}
	_ adapterhttp.StoreDirectory = postgres.StoreRegistry{}
)
