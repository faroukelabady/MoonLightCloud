package postgres

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

type projectorSkewClock struct{ offset atomic.Int64 }

func (c *projectorSkewClock) Now() time.Time {
	return time.Now().Add(time.Duration(c.offset.Load()))
}

type countingSkewStore struct {
	Devices
	scans    atomic.Int64
	projects atomic.Int64
}

func (s *countingSkewStore) PendingCatalogEvents(ctx context.Context, processor, eventType string, limit int, asOf time.Time) ([]string, error) {
	s.scans.Add(1)
	return s.Devices.PendingCatalogEvents(ctx, processor, eventType, limit, asOf)
}

func (s *countingSkewStore) ProjectProductVariantInventory(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	s.projects.Add(1)
	return s.Devices.ProjectProductVariantInventory(ctx, event, now)
}

// Use actual database discovery/claiming, including the new local retry
// cap. An empty fake discovery cannot reproduce F18's inner drain loop.
func TestCatalogProjectorClockSkewYieldsAndRecovers(t *testing.T) {
	for _, offset := range []time.Duration{-time.Minute, time.Minute} {
		for _, previousAttempts := range []int{0, 7} {
			t.Run(fmt.Sprintf("offset=%s/attempts=%d", offset, previousAttempts), func(t *testing.T) {
				env := openSaleEnv(t)
				_, productID := seedCatalogProduct(t, env, 48000, "ML-SKEW-P")
				variantID := catalogIDs(t, 48010, "variant")["variant"]
				eventID := variantEventID(48020)
				ingestCatalog(t, env, eventID, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
					variantInventoryPayload(variantID, productID, "ML-SKEW-V", 4, 8, true, nil))
				if previousAttempts > 0 {
					_, err := env.pool.Exec(context.Background(), `INSERT INTO sync_event_processing
						(event_id,processor,status,attempt_count) VALUES ($1,$2,'pending',$3)`,
						eventID, catalog.ProcessorProductVariantInventoryProjectionV1, previousAttempts)
					if err != nil {
						t.Fatal(err)
					}
				}
				c := &projectorSkewClock{}
				c.offset.Store(int64(offset))
				store := &countingSkewStore{Devices: catalogStore(env)}
				event, ok, err := store.LoadCatalogEvent(context.Background(), eventID)
				if err != nil || !ok {
					t.Fatalf("load: found=%v err=%v", ok, err)
				}
				result, err := store.ProjectProductVariantInventory(context.Background(), event, c.Now())
				if err == nil || result.Outcome != catalog.OutcomeRetryable || result.ErrorCode != ErrCatalogDependencyWait {
					t.Fatalf("persist dependency retry: result=%+v err=%v", result, err)
				}
				store.projects.Store(0)
				p := catalog.NewProductVariantInventoryProjector(store, c, nilLogger())
				// Observe strictly before the first 500ms durable local deadline.
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				p.Run(ctx)
				cancel()
				if scans, projects := store.scans.Load(), store.projects.Load(); scans > 2 || projects > 1 {
					t.Fatalf("unbounded no-progress drain: scans=%d projects=%d", scans, projects)
				}
				var attempts int
				var status string
				if err := env.pool.QueryRow(context.Background(), `SELECT attempt_count,status FROM sync_event_processing
					WHERE event_id=$1 AND processor=$2`, eventID, catalog.ProcessorProductVariantInventoryProjectionV1).Scan(&attempts, &status); err != nil {
					t.Fatal(err)
				}
				if attempts != previousAttempts+1 || status != "retry" {
					t.Fatalf("not-due work changed: attempts=%d status=%s", attempts, status)
				}
				t.Logf("offset=%s attempt=%d scans=%d projects=%d; durable retry preserved", offset, attempts, store.scans.Load(), store.projects.Load())
				if offset > 0 {
					// A future deadline remains future for PostgreSQL; no early
					// processing or forced deadline reset is appropriate.
					return
				}

				// Release the actual dependency and align the application clock.
				// Restart from durable work, without resetting its retry deadline.
				variantEvent := variantEventID(48021)
				ingestCatalog(t, env, variantEvent, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:01:00Z",
					variantPayload(variantID, productID, "ML-SKEW-V", true, false, "", nil, 1))
				if res := projectCatalogOnce(t, env, variantEvent); res.Outcome != catalog.OutcomeProcessed {
					t.Fatalf("dependency projection: %+v", res)
				}
				c.offset.Store(0)
				recoveryCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				var progressed atomic.Bool
				p.WakeOnProgress(func() { progressed.Store(true); stop() })
				p.Run(recoveryCtx)
				if !progressed.Load() {
					t.Fatal("due work stranded after dependency/clock recovery")
				}
				var stock int64
				if err := env.pool.QueryRow(context.Background(), `SELECT stock_quantity FROM catalog_product_variant_inventory WHERE variant_id=$1`, variantID).Scan(&stock); err != nil || stock != 4 {
					t.Fatalf("recovered stock=%d err=%v", stock, err)
				}
			})
		}
	}
}

// A full batch of dependency waits must not hide healthy work when the
// database clock is either ahead of or behind the application clock.
func TestCatalogProjectorHealthyInventoryBehindRetriesAcrossClocks(t *testing.T) {
	for _, offset := range []time.Duration{-time.Minute, 0, time.Minute} {
		t.Run(offset.String(), func(t *testing.T) {
			env := openSaleEnv(t)
			_, productID := seedCatalogProduct(t, env, 61000, "ML-BACKLOG-P")
			healthy := catalogIDs(t, 61100, "healthy")["healthy"]
			variantEvent := variantEventID(61101)
			ingestCatalog(t, env, variantEvent, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
				variantPayload(healthy, productID, "ML-HEALTHY", true, false, "", nil, 1))
			if res := projectCatalogOnce(t, env, variantEvent); res.Outcome != catalog.OutcomeProcessed {
				t.Fatalf("variant: %+v", res)
			}
			for i := 0; i < 25; i++ {
				missing := catalogIDs(t, 61200+i, "missing")["missing"]
				ingestCatalog(t, env, variantEventID(61200+i), catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
					variantInventoryPayload(missing, productID, "ML-MISSING", 4, 8, true, nil))
			}
			ingestCatalog(t, env, variantEventID(61300), catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:01:00Z",
				variantInventoryPayload(healthy, productID, "ML-HEALTHY", 9, 8, true, nil))
			store := &countingSkewStore{Devices: catalogStore(env)}
			c := &projectorSkewClock{}
			c.offset.Store(int64(offset))
			p := catalog.NewProductVariantInventoryProjector(store, c, nilLogger())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p.WakeOnProgress(cancel)
			p.Run(ctx)
			var stock int64
			if err := env.pool.QueryRow(context.Background(), `SELECT stock_quantity FROM catalog_product_variant_inventory WHERE variant_id=$1`, healthy).Scan(&stock); err != nil || stock != 9 {
				t.Fatalf("healthy work stranded: stock=%d err=%v", stock, err)
			}
			var retries int
			if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_event_processing WHERE processor=$1 AND status='retry'`, catalog.ProcessorProductVariantInventoryProjectionV1).Scan(&retries); err != nil || retries != 25 {
				t.Fatalf("durable waits=%d err=%v", retries, err)
			}
			t.Logf("offset=%s healthy stock=%d; 25 prior waits preserved; scans=%d projects=%d", offset, stock, store.scans.Load(), store.projects.Load())
		})
	}
}
