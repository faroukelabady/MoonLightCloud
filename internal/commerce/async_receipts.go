package commerce

import "context"

// AsyncProductReceipt is acknowledged provider work, distinct from an
// uncertain physical request. The matching product coordinator owns writes.
type AsyncProductReceipt struct {
	Role, Intent, OperationID, State, ProductID string
}

type AsyncProductReceipts interface {
	AcknowledgeAsync(context.Context, string, AsyncProductReceipt) error
	LoadAsync(context.Context, string) (AsyncProductReceipt, bool, error)
	// FinishAsync settles pending work, or fills a missing Product identity
	// on a legacy failed receipt without changing its outcome or known ID.
	FinishAsync(context.Context, string, string, string) error
}

func ProductAsyncReceipts(ctx context.Context) (AsyncProductReceipts, error) {
	store, ok := ctx.Value(productMutationBarrierKey{}).(AsyncProductReceipts)
	if !ok {
		return nil, ValidationError("durable asynchronous receipt store required")
	}
	return store, nil
}
