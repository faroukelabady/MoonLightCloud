package postgres

import (
	"context"
	"errors"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (b *productMutationBarrier) AcknowledgeAsync(ctx context.Context, token string, receipt commerce.AsyncProductReceipt) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.token.Valid || uuidString(b.token) != token {
		return commerce.ErrMutationUncertain()
	}
	if err := commerce.ProductSyncGuard(ctx); err != nil {
		return err
	}
	bounded, cancel := b.store.ctx(ctx)
	defer cancel()
	n, err := sqlcgen.New(b.store.pool).AcknowledgeCommerceAsyncReceipt(bounded, sqlcgen.AcknowledgeCommerceAsyncReceiptParams{
		OperationID: b.token, ProviderKey: string(b.provider), ProductID: b.product,
		AsyncRole: pgText(receipt.Role), AsyncIntent: pgText(receipt.Intent), ProviderOperationID: pgText(receipt.OperationID),
	})
	if err != nil {
		return redact(err)
	}
	if n != 1 {
		return commerce.ErrMutationUncertain()
	}
	b.token = pgtype.UUID{}
	return nil
}

func (b *productMutationBarrier) LoadAsync(ctx context.Context, role string) (commerce.AsyncProductReceipt, bool, error) {
	if err := commerce.ProductSyncGuard(ctx); err != nil {
		return commerce.AsyncProductReceipt{}, false, err
	}
	bounded, cancel := b.store.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(b.store.pool).GetCommerceAsyncReceipt(bounded, sqlcgen.GetCommerceAsyncReceiptParams{ProviderKey: string(b.provider), ProductID: b.product, AsyncRole: pgText(role)})
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.AsyncProductReceipt{}, false, nil
	}
	if err != nil {
		return commerce.AsyncProductReceipt{}, false, redact(err)
	}
	return commerce.AsyncProductReceipt{Role: row.AsyncRole.String, Intent: row.AsyncIntent.String, OperationID: row.ProviderOperationID.String, State: row.AsyncState.String, ProductID: row.AsyncProductID.String}, true, nil
}

func (b *productMutationBarrier) FinishAsync(ctx context.Context, operation, state, product string) error {
	if err := commerce.ProductSyncGuard(ctx); err != nil {
		return err
	}
	bounded, cancel := b.store.ctx(ctx)
	defer cancel()
	n, err := sqlcgen.New(b.store.pool).FinishCommerceAsyncReceipt(bounded, sqlcgen.FinishCommerceAsyncReceiptParams{ProviderKey: string(b.provider), ProductID: b.product, ProviderOperationID: pgText(operation), AsyncState: pgText(state), AsyncProductID: pgText(product)})
	if err != nil {
		return redact(err)
	}
	if n != 1 {
		return commerce.ConflictError("asynchronous receipt superseded")
	}
	return nil
}
