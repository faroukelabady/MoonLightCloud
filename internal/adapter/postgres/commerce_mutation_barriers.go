package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type productMutationBarrier struct {
	store    Devices
	provider commerce.ProviderKey
	product  pgtype.UUID
	mu       sync.Mutex
	token    pgtype.UUID
}

func (b *productMutationBarrier) Begin(ctx context.Context, fingerprint string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token.Valid {
		return "", commerce.ErrMutationUncertain()
	}
	if err := commerce.ProductSyncGuard(ctx); err != nil {
		return "", err
	}
	token, err := parseUUID(ids.System{}.New())
	if err != nil {
		return "", err
	}
	// This uses a separate autocommit connection, not the advisory transaction.
	// Therefore a crash or rollback cannot erase send-before-write evidence.
	bounded, cancel := b.store.ctx(ctx)
	defer cancel()
	n, err := sqlcgen.New(b.store.pool).BeginCommerceMutationBarrier(bounded, sqlcgen.BeginCommerceMutationBarrierParams{OperationID: token, ProviderKey: string(b.provider), ProductID: b.product, RequestFingerprint: fingerprint})
	if err != nil {
		return "", redact(err)
	}
	if n != 1 {
		return "", commerce.ErrMutationUncertain()
	}
	b.token = token
	// Recheck after the independent insert: a lost advisory session between
	// check and persistence must never authorize a later HTTP request.
	if err := commerce.ProductSyncGuard(ctx); err != nil {
		return "", err
	}
	return uuidString(token), nil
}
func (b *productMutationBarrier) Complete(_ context.Context, token string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.token.Valid || uuidString(b.token) != token {
		return commerce.ErrMutationUncertain()
	}
	// Valid response evidence may arrive at cancellation. Clear only this exact
	// request; cleanup must not inherit a cancelled context or touch a newer row.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := sqlcgen.New(b.store.pool).CompleteCommerceMutationBarrier(ctx, sqlcgen.CompleteCommerceMutationBarrierParams{OperationID: b.token, ProviderKey: string(b.provider), ProductID: b.product})
	if err != nil {
		return redact(err)
	}
	if n != 1 {
		return commerce.ErrMutationUncertain()
	}
	b.token = pgtype.UUID{}
	return nil
}
func (b *productMutationBarrier) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.token.Valid
}
func (b *productMutationBarrier) retain() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.token.Valid {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Failure here is safe: an in_flight row blocks just as an uncertain row does.
	_, _ = sqlcgen.New(b.store.pool).MarkCommerceMutationUncertain(ctx, sqlcgen.MarkCommerceMutationUncertainParams{OperationID: b.token, ProviderKey: string(b.provider), ProductID: b.product})
}
func (d Devices) GetProductMutationBarrier(ctx context.Context, provider commerce.ProviderKey, product string) (commerce.MutationBarrierStatus, error) {
	if _, err := commerce.ValidateProviderKey(string(provider)); err != nil {
		return commerce.MutationBarrierStatus{}, commerce.ValidationError("invalid provider key")
	}
	id, err := parseUUID(product)
	if err != nil {
		return commerce.MutationBarrierStatus{}, commerce.ValidationError("product_id must be a UUID")
	}
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).GetActiveCommerceMutationBarrier(ctx, sqlcgen.GetActiveCommerceMutationBarrierParams{ProviderKey: string(provider), ProductID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.MutationBarrierStatus{}, nil
	}
	if err != nil {
		return commerce.MutationBarrierStatus{}, redact(err)
	}
	return commerce.MutationBarrierStatus{OperationID: uuidString(row.OperationID), ProviderKey: provider, ProductID: product, State: row.State}, nil
}

// Resolve requires explicit operator confirmation of remote settlement, never
// a local timeout, fresh GET or observed expiry. It uses the same sequence lock
// and expected operation identity; a late resolver cannot release newer work.
func (d Devices) ResolveProductMutationBarrier(ctx context.Context, provider commerce.ProviderKey, product, operation, resolution string, confirmed bool) error {
	if !confirmed || (resolution != "remote_completed" && resolution != "remote_not_applied") {
		return commerce.ValidationError("explicit remote settlement confirmation required")
	}
	if _, err := commerce.ValidateProviderKey(string(provider)); err != nil {
		return commerce.ValidationError("invalid provider key")
	}
	productUUID, err := parseUUID(product)
	if err != nil {
		return commerce.ValidationError("product_id must be a UUID")
	}
	operationUUID, err := parseUUID(operation)
	if err != nil {
		return commerce.ValidationError("operation_id must be a UUID")
	}
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return redact(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	digest := sha256.Sum256([]byte("commerce-product\x00" + string(provider) + "\x00" + uuidString(productUUID)))
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(digest[:8]))); err != nil {
		return redact(err)
	}
	n, err := sqlcgen.New(tx).ResolveCommerceMutationBarrier(ctx, sqlcgen.ResolveCommerceMutationBarrierParams{OperationID: operationUUID, ProviderKey: string(provider), ProductID: productUUID, Resolution: pgtype.Text{String: resolution, Valid: true}})
	if err != nil {
		return redact(err)
	}
	if n != 1 {
		prior, err := sqlcgen.New(tx).GetCommerceMutationBarrierResolution(ctx, sqlcgen.GetCommerceMutationBarrierResolutionParams{OperationID: operationUUID, ProviderKey: string(provider), ProductID: productUUID})
		if err != nil || !prior.Valid || prior.String != resolution {
			return commerce.ConflictError("commerce mutation barrier changed or already resolved")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return redact(err)
	}
	return nil
}
