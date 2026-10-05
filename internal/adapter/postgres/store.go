package postgres

import (
	"context"
	"errors"
	"strings"

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
	status, err := q.LockStoreRegistrationDevice(ctx, duid)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return store.RegistrationResult{}, apperr.New(apperr.Unauthorized, "device is not active")
	}
	if err != nil {
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	request.StoreID = uuidString(suid)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Timezone = strings.TrimSpace(request.Timezone)
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
	// Unbound devices may bootstrap only a genuinely new Store. The insert's
	// affected-row count also rejects a concurrent loser without enrolling it.
	inserted, err := q.InsertStore(ctx, sqlcgen.InsertStoreParams{
		ID: suid, DisplayName: request.DisplayName, Timezone: request.Timezone,
	})
	if err != nil {
		return store.RegistrationResult{}, apperr.Wrap(apperr.Unavailable, "registration temporarily unavailable", redact(err))
	}
	if inserted != 1 {
		return store.RegistrationResult{}, apperr.New(apperr.Conflict, "STORE_ENROLLMENT_REQUIRED")
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

// EnrollStore is an operator-only enrollment seam used by the trusted CLI.
// It never changes Store metadata or replaces an existing device binding.
func (d Devices) EnrollStore(ctx context.Context, deviceID, storeID string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	suid, err := parseUUID(storeID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "store id must be a UUID")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Unavailable, "enrollment temporarily unavailable", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	status, err := q.LockStoreRegistrationDevice(ctx, duid)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.New(apperr.NotFound, "device not found")
	}
	if err != nil {
		return apperr.Wrap(apperr.Unavailable, "enrollment temporarily unavailable", redact(err))
	}
	if status != "active" {
		return apperr.New(apperr.Conflict, "DEVICE_NOT_ACTIVE")
	}
	if _, err := q.StoreByID(ctx, suid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.NotFound, "store not found")
		}
		return apperr.Wrap(apperr.Unavailable, "enrollment temporarily unavailable", redact(err))
	}
	if binding, err := q.BindingByDevice(ctx, duid); err == nil {
		if uuidString(binding.StoreID) != uuidString(suid) {
			return apperr.New(apperr.Conflict, ErrStoreBindingConflict)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return apperr.Wrap(apperr.Unavailable, "enrollment temporarily unavailable", redact(err))
	}
	if err := q.InsertDeviceBinding(ctx, sqlcgen.InsertDeviceBindingParams{DeviceID: duid, StoreID: suid}); err != nil {
		return apperr.Wrap(apperr.Unavailable, "enrollment temporarily unavailable", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.Unavailable, "enrollment temporarily unavailable", redact(err))
	}
	return nil
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

// CatalogAdminBindingStore resolves the current Store binding for one
// device, or "" when unbound. Read-only; bindings are immutable.
func (d Devices) CatalogAdminBindingStore(ctx context.Context, deviceID string) (string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return "", err
	}
	binding, err := sqlcgen.New(d.pool).BindingByDevice(ctx, duid)
	if err != nil {
		return "", nil
	}
	return uuidString(binding.StoreID), nil
}
