package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Phase 13 §83: durable commerce re-evaluation queue (read/write). All
// operations are narrow, bounded and multi-instance safe (atomic
// SKIP-LOCKED claim with lease). Store ownership rides the Product row;
// nothing here contacts providers.

func (d Devices) ClaimProductReevaluations(ctx context.Context, limit int, leaseUntil time.Time) ([]commerce.ProductReevaluation, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	token, err := parseUUID(ids.System{}.New())
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ClaimProductReevaluations(ctx, sqlcgen.ClaimProductReevaluationsParams{
		BatchSize: int32(limit), LeaseUntil: pgTime(leaseUntil), LeaseToken: token,
	})
	if err != nil {
		return nil, redact(err)
	}
	out := make([]commerce.ProductReevaluation, 0, len(rows))
	for _, row := range rows {
		out = append(out, commerce.ProductReevaluation{
			ProductID:         uuidString(row.ProductID),
			StoreID:           uuidPtr(row.StoreID),
			Reason:            row.Reason,
			Attempts:          row.Attempts,
			ClaimedGeneration: row.ClaimedGeneration.Int64, LeaseGeneration: row.LeaseGeneration,
			LeaseToken: uuidString(row.LeaseToken), LeaseUntil: row.LeaseUntil.Time,
		})
	}
	return out, nil
}

// Completion and retry return false for stale or expired claims. The opaque
// token also distinguishes a deleted/reinserted Product queue row.
func (d Devices) CompleteProductReevaluation(ctx context.Context, claim commerce.ProductReevaluation) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(claim.ProductID)
	if err != nil {
		return false, err
	}
	token, err := parseUUID(claim.LeaseToken)
	if err != nil {
		return false, err
	}
	acknowledged, err := sqlcgen.New(d.pool).CompleteProductReevaluation(ctx, sqlcgen.CompleteProductReevaluationParams{
		ProductID: uid, LeaseToken: token, LeaseGeneration: claim.LeaseGeneration, ClaimedGeneration: claim.ClaimedGeneration,
	})
	if err != nil {
		return false, redact(err)
	}
	return acknowledged.Bool, nil
}

func (d Devices) RetryProductReevaluation(ctx context.Context, claim commerce.ProductReevaluation, next time.Time, code string) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(claim.ProductID)
	if err != nil {
		return false, err
	}
	token, err := parseUUID(claim.LeaseToken)
	if err != nil {
		return false, err
	}
	rows, err := sqlcgen.New(d.pool).RetryProductReevaluation(ctx, sqlcgen.RetryProductReevaluationParams{
		ProductID: uid, LeaseToken: token, LeaseGeneration: claim.LeaseGeneration, ClaimedGeneration: claim.ClaimedGeneration,
		NextAttemptAt: pgTime(next), LastErrorCode: code,
	})
	if err != nil {
		return false, redact(err)
	}
	return rows == 1, nil
}

// EnqueueProductReevaluation records one Product re-evaluation request
// (coalescing: repeats collapse into the single Product row). Used by
// the product projector when classification changes and by operators.
func (d Devices) EnqueueProductReevaluation(ctx context.Context, productID string, storeID *string, reason string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(productID)
	if err != nil {
		return err
	}
	params := sqlcgen.EnqueueProductReevaluationParams{ProductID: uid, Reason: reason}
	if storeID != nil && *storeID != "" {
		suid, err := parseUUID(*storeID)
		if err != nil {
			return err
		}
		params.StoreID = suid
	}
	if err := sqlcgen.New(d.pool).EnqueueProductReevaluation(ctx, params); err != nil {
		return redact(err)
	}
	return nil
}

// EnqueueCategoryAffectedReevaluations enqueues every Product whose
// classification reaches the Category or any DAG descendant of it
// (set-based, Store-proven only). Returns the number of rows scheduled.
func (d Devices) EnqueueCategoryAffectedReevaluations(ctx context.Context, categoryID string, storeID *string, reason string) (int64, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	cuid, err := parseUUID(categoryID)
	if err != nil {
		return 0, err
	}
	params := sqlcgen.EnqueueCategoryAffectedReevaluationsParams{Column1: cuid, Column2: reason}
	if storeID != nil && *storeID != "" {
		suid, err := parseUUID(*storeID)
		if err != nil {
			return 0, err
		}
		params.Column3 = suid
	}
	return sqlcgen.New(d.pool).EnqueueCategoryAffectedReevaluations(ctx, params)
}

var _ commerce.ProductReevaluationStore = (*Devices)(nil)
