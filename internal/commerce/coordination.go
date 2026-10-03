package commerce

import "context"

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
	return ok && scope.provider == provider && scope.product == product
}

// CoordinatedProductContext is supplied only while the durable coordinator
// owns the matching lock. It avoids reacquiring it in nested adapter calls.
func CoordinatedProductContext(ctx context.Context, provider ProviderKey, product string) context.Context {
	return context.WithValue(ctx, productSyncKey{}, productSyncScope{provider, product})
}
