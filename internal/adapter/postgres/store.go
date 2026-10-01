package postgres

import (
	"context"
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	"github.com/jackc/pgx/v5"
)

// Registration conflict code (stable, operator-facing).
const ErrStoreBindingConflict = "STORE_BINDING_CONFLICT"

// RegisterStore binds the authenticated device to the presented Store.
// First binding wins permanently: a bound device presenting a different
// Store gets a deterministic conflict, never a silent rebind. Store
// metadata follows last-writer-wins on the presented values; identity
// never changes. All writes happen in one short transaction; no locks
// are held across network calls.
func (d Devices) RegisterStore(ctx context.Context, deviceID string, request store.RegistrationRequest) (store.RegistrationResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if err := store.ValidateRegistration(request); err != nil {
		return store.RegistrationResult{}, err
	}
	duid, err := parseUUID(deviceID)
	if err != nil {
		return store.RegistrationResult{}, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	suid, err := parseUUID(request.StoreID)
	if err != nil {
		return store.RegistrationResult{}, apperr.New(apperr.InvalidInput, "store_id must be a UUID")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if existing, err := q.BindingByDevice(ctx, duid); err == nil {
		if uuidString(existing.StoreID) != request.StoreID {
			_ = tx.Rollback(ctx)
			return store.RegistrationResult{}, apperr.New(apperr.Conflict, ErrStoreBindingConflict)
		}
		// Idempotent re-registration: converge mutable metadata.
		if err := q.UpdateStoreMetadata(ctx, sqlcgen.UpdateStoreMetadataParams{
			ID: suid, DisplayName: request.DisplayName, Timezone: request.Timezone,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
		}
		if err := tx.Commit(ctx); err != nil {
			return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
		}
		return store.RegistrationResult{StoreID: request.StoreID, DisplayName: request.DisplayName, Timezone: request.Timezone, Bound: true}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	// Unbound device: establish the store row (or adopt an existing one
	// provisioned by another device of the same store) and bind.
	if err := q.InsertStore(ctx, sqlcgen.InsertStoreParams{
		ID: suid, DisplayName: request.DisplayName, Timezone: request.Timezone,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	if err := q.UpdateStoreMetadata(ctx, sqlcgen.UpdateStoreMetadataParams{
		ID: suid, DisplayName: request.DisplayName, Timezone: request.Timezone,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	if err := q.InsertDeviceBinding(ctx, sqlcgen.InsertDeviceBindingParams{
		DeviceID: duid, StoreID: suid,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	// Lost a concurrent race iff the durable binding names another store.
	binding, err := q.BindingByDevice(ctx, duid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	if uuidString(binding.StoreID) != request.StoreID {
		_ = tx.Rollback(ctx)
		return store.RegistrationResult{}, apperr.New(apperr.Conflict, ErrStoreBindingConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	return store.RegistrationResult{StoreID: request.StoreID, DisplayName: request.DisplayName, Timezone: request.Timezone, Bound: true}, nil
}

// DeviceStoreInfo is one device's authoritative Store context for
// operator display. Unbound devices simply have no entry.
type DeviceStoreInfo struct {
	DeviceID    string
	StoreID     string
	DisplayName string
	Status      string
}

// BindingsWithStores returns every device binding with its Store display
// metadata in one bounded read (stores are few; no per-device queries).
func (d Devices) BindingsWithStores(ctx context.Context) ([]DeviceStoreInfo, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListBindingsWithStores(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list store bindings", redact(err))
	}
	out := make([]DeviceStoreInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, DeviceStoreInfo{
			DeviceID: uuidString(row.DeviceID), StoreID: uuidString(row.StoreID),
			DisplayName: row.DisplayName, Status: row.Status,
		})
	}
	return out, nil
}

// StoreSummaries lists stores with device counts in one aggregate read.
func (d Devices) StoreSummaries(ctx context.Context) ([]store.Summary, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListStores(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list stores", redact(err))
	}
	out := make([]store.Summary, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.Summary{
			ID: row.ID.String(), DisplayName: row.DisplayName, Timezone: row.Timezone,
			Status: row.Status, DeviceCount: row.DeviceCount,
			CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return out, nil
}
