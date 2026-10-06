package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// Worker paths return data only: the parent test/cleanup owns all failures.
type catalogWorkerResult struct {
	Result   catalog.ProjectResult
	Attempts int
	Err      error
}

func reportCatalogWorkerError(t *testing.T, err error) {
	if err != nil {
		t.Cleanup(func() { t.Errorf("catalog worker failure: %v", err) })
	}
}
func catalogWorkerAttempt(d Devices, id, typ string) catalogWorkerResult {
	ctx := context.Background()
	rec, ok, err := d.LoadCatalogEvent(ctx, id)
	if err != nil || !ok {
		return catalogWorkerResult{Attempts: 1, Err: fmt.Errorf("event %s projector %s load found=%v: %w", id, typ, ok, err)}
	}
	if typ != "" && rec.EventType != typ {
		return catalogWorkerResult{Attempts: 1, Err: fmt.Errorf("event %s projector %s unexpected type %s", id, typ, rec.EventType)}
	}
	var res catalog.ProjectResult
	switch rec.EventType {
	case catalog.EventCategorySnapshotV1, catalog.EventCategorySnapshotV2:
		res, err = d.ProjectCategory(ctx, rec, time.Now())
	case catalog.EventTagSnapshotV1:
		res, err = d.ProjectTag(ctx, rec, time.Now())
	case catalog.EventProductSnapshotV1, catalog.EventProductSnapshotV2:
		res, err = d.ProjectProduct(ctx, rec, time.Now())
	case catalog.EventProductSalesPolicySnapshotV1:
		res, err = d.ProjectProductSalesPolicy(ctx, rec, time.Now())
	case catalog.EventProductConfigurationSnapshotV1:
		res, err = d.ProjectProductConfigurations(ctx, rec, time.Now())
	case catalog.EventInventoryProductSnapshotV1:
		res, err = d.ProjectProductInventory(ctx, rec, time.Now())
	case catalog.EventProductVariantSnapshotV1:
		res, err = d.ProjectProductVariant(ctx, rec, time.Now())
	case catalog.EventInventoryProductVariantSnapshotV1:
		res, err = d.ProjectProductVariantInventory(ctx, rec, time.Now())
	default:
		err = fmt.Errorf("unexpected projector %s", rec.EventType)
	}
	// Normalize only the two raw/wrapped SQL aborts. Other errors retain their
	// original pair and can never masquerade as a no-error waiting outcome.
	if err != nil && catalogAttemptRetryable(res, err) {
		if res.Outcome == 0 {
			res.Outcome = catalog.OutcomeRetryable
		}
		return catalogWorkerResult{Result: res, Attempts: 1, Err: err}
	}
	return catalogWorkerResult{Result: res, Attempts: 1, Err: err}
}
func runCatalogAttempts(id, typ string, budget int, attempt func() catalogWorkerResult) catalogWorkerResult {
	var last catalogWorkerResult
	for i := 1; i <= budget; i++ {
		last = attempt()
		last.Attempts = i
		if last.Err != nil {
			if catalogAttemptRetryable(last.Result, last.Err) {
				continue
			}
			last.Err = fmt.Errorf("event %s projector %s attempt %d: %w (sqlstate=%s)", id, typ, i, last.Err, sqlStateOf(last.Err))
			return last
		}
		switch last.Result.Outcome {
		case catalog.OutcomeProcessed, catalog.OutcomeAlready, catalog.OutcomeBlocked:
			return last
		case catalog.OutcomeNotDue:
			continue // durable backoff wait, expired on next attempt
		default:
			last.Err = fmt.Errorf("event %s projector %s attempt %d unexpected no-error outcome %+v", id, typ, i, last.Result)
			return last
		}
	}
	last.Err = fmt.Errorf("event %s projector %s exhausted %d attempts: last outcome=%+v failure=%v", id, typ, budget, last.Result, last.Err)
	return last
}
func runCatalogWorker(d Devices, id, typ string, budget int) catalogWorkerResult {
	return runCatalogAttempts(id, typ, budget, func() catalogWorkerResult {
		_, err := d.pool.Exec(context.Background(), `UPDATE sync_event_processing SET next_attempt_at=NULL WHERE event_id=$1 AND status='retry'`, id)
		if err != nil {
			return catalogWorkerResult{Err: err}
		}
		return catalogWorkerAttempt(d, id, typ)
	})
}
