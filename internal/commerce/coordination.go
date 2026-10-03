package commerce

import (
	"context"
	"github.com/google/uuid"
)

// ProductSyncCoordinator serializes the complete remote mutation sequence,
// including the desired-state read and mapping persistence, across instances.
// Provider interfaces and their request contracts remain unchanged.
type ProductSyncCoordinator interface {
	WithProductSync(context.Context, ProviderKey, string, func(context.Context) error) error
}

type productSyncScope struct {
	provider ProviderKey
	product  string
}
type productSyncKey struct{}
type productSyncGuardKey struct{}

// ProductSyncGuard detects a lost database session before a provider call.
func ProductSyncGuard(ctx context.Context) error {
	if guard, ok := ctx.Value(productSyncGuardKey{}).(func(context.Context) error); ok {
		return guard(ctx)
	}
	return ctx.Err()
}

func WithProductSyncGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, productSyncGuardKey{}, guard)
}

func ProductSyncHeld(ctx context.Context, provider ProviderKey, product string) bool {
	scope, ok := ctx.Value(productSyncKey{}).(productSyncScope)
	if !ok || scope.provider != provider {
		return false
	}
	if scope.product == product {
		return true
	}
	a, ae := uuid.Parse(scope.product)
	b, be := uuid.Parse(product)
	return ae == nil && be == nil && a == b
}

// CoordinatedProductContext is supplied only while the durable coordinator
// owns the matching lock. It avoids reacquiring it in nested adapter calls.
func CoordinatedProductContext(ctx context.Context, provider ProviderKey, product string) context.Context {
	return context.WithValue(ctx, productSyncKey{}, productSyncScope{provider, product})
}

// ProductMutationBarrier records each physical request durably before send.
// Complete is called only for definitive remote outcome evidence. Absence of
// evidence leaves the barrier intact; cancellation never means remote abort.
type ProductMutationBarrier interface {
	Begin(context.Context, string) (string, error)
	Complete(context.Context, string) error
}
type productMutationBarrierKey struct{}

func WithProductMutationBarrier(ctx context.Context, barrier ProductMutationBarrier) context.Context {
	return context.WithValue(ctx, productMutationBarrierKey{}, barrier)
}
func BeginProductMutation(ctx context.Context, fingerprint string) (string, error) {
	barrier, ok := ctx.Value(productMutationBarrierKey{}).(ProductMutationBarrier)
	if !ok {
		return "", ValidationError("commerce mutation barrier required")
	}
	return barrier.Begin(ctx, fingerprint)
}
func CompleteProductMutation(ctx context.Context, token string) error {
	barrier, ok := ctx.Value(productMutationBarrierKey{}).(ProductMutationBarrier)
	if !ok {
		return ValidationError("commerce mutation barrier required")
	}
	return barrier.Complete(ctx, token)
}
func ErrMutationUncertain() error { return ConflictError("COMMERCE_MUTATION_UNCERTAIN") }

type MutationBarrierStatus struct {
	OperationID string
	ProviderKey ProviderKey
	ProductID   string
	State       string
}
