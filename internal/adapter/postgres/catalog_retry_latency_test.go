package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCatalogLocalRetryDelayClassification(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		cause      error
		local      bool
	}{
		{"dependency", ErrCatalogDependencyWait, nil, true},
		{"serialization", ErrProjection, &pgconn.PgError{Code: "40001"}, true},
		{"wrapped deadlock", ErrProjection, fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"unique", ErrProjection, &pgconn.PgError{Code: "23505"}, false},
		{"schema", ErrProjection, &pgconn.PgError{Code: "42703"}, false},
		{"other transaction", ErrProjection, &pgconn.PgError{Code: "40002"}, false},
		{"network outage", ErrProjection, errors.New("network unavailable"), false},
		{"expired context", ErrProjection, context.DeadlineExceeded, false},
		{"unclassified", ErrProjection, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, attempt := range []int{-1, 0, 1, 2, 3, 6, 20, 1 << 30} {
				want := catalog.Backoff(attempt)
				if tc.local {
					want = 2 * time.Second
					if attempt <= 0 {
						want = 500 * time.Millisecond
					} else if attempt == 1 {
						want = time.Second
					}
				}
				if got := catalogRetryDelay(attempt, tc.code, tc.cause); got != want {
					t.Fatalf("attempt=%d delay=%v want=%v", attempt, got, want)
				}
			}
		})
	}
}

// Advance the caller's clock to the durable deadline. Never clear or override
// next_attempt_at: this exercises the production scheduling contract directly.
func catalogRetryDeadline(t *testing.T, env *saleEnv, event, processor string, now time.Time, wantAttempt int, limits ...time.Duration) time.Time {
	t.Helper()
	var next time.Time
	var count int
	var status string
	if err := env.pool.QueryRow(context.Background(), `SELECT status, attempt_count, next_attempt_at FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, event, processor).Scan(&status, &count, &next); err != nil {
		t.Fatal(err)
	}
	limit := 2 * time.Second
	if len(limits) > 0 {
		limit = limits[0]
	}
	if status != catalog.ProcRetry || count != wantAttempt || !next.After(now) || next.Sub(now) > limit {
		t.Fatalf("status=%s attempts=%d next delay=%v", status, count, next.Sub(now))
	}
	return next
}

func TestCatalogDependencyRetriesUseDurableBoundedDeadlines(t *testing.T) {
	env := openSaleEnv(t)
	_, product := seedCatalogProduct(t, env, 45000, "ML-LOCAL-RETRY")
	variant := catalogIDs(t, 45010, "v")["v"]
	event := variantEventID(45011)
	ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:00:00Z", variantInventoryPayload(variant, product, "ML-LOCAL-V", 4, 1, true, nil))
	d := catalogStore(env)
	record, ok, err := d.LoadCatalogEvent(context.Background(), event)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	start := now
	for attempt := 1; attempt <= 7; attempt++ {
		res, err := d.ProjectProductVariantInventory(context.Background(), record, now)
		if err == nil || res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
			t.Fatalf("attempt=%d result=%+v err=%v", attempt, res, err)
		}
		next := catalogRetryDeadline(t, env, event, catalog.ProcessorProductVariantInventoryProjectionV1, now, attempt)
		res, err = d.ProjectProductVariantInventory(context.Background(), record, next.Add(-time.Microsecond))
		if err != nil || res.Outcome != catalog.OutcomeNotDue {
			t.Fatalf("early retry result=%+v err=%v", res, err)
		}
		now = next
	}
	if now.Sub(start) != 11500*time.Millisecond {
		t.Fatalf("seven local waits=%v, want11.5s rather than155s", now.Sub(start))
	}
	variantEvent := variantEventID(45012)
	ingestCatalog(t, env, variantEvent, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:01:00Z", variantPayload(variant, product, "ML-LOCAL-V", true, false, "color=blue", nil, 1))
	if res := projectCatalogOnce(t, env, variantEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	res, err := d.ProjectProductVariantInventory(context.Background(), record, now)
	if err != nil || res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("converge result=%+v err=%v", res, err)
	}
	var stock, revision int64
	if err := env.pool.QueryRow(context.Background(), `SELECT stock_quantity, source_revision FROM catalog_product_variant_inventory WHERE variant_id=$1`, variant).Scan(&stock, &revision); err != nil || stock != 4 || revision != 1 {
		t.Fatalf("stock=%d revision=%d err=%v", stock, revision, err)
	}
}

func TestCatalogDependencyChainConvergesAtBoundedDurableDeadlines(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 47000, "root", "child", "type", "product", "variant")
	var product map[string]any
	if err := json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &product); err != nil {
		t.Fatal(err)
	}
	product["product_id"], product["top_category_id"], product["product_type_id"] = ids["product"], ids["root"], ids["type"]
	product["subcategory_ids"] = []string{ids["child"]}
	productRaw, err := json.Marshal(product)
	if err != nil {
		t.Fatal(err)
	}
	d := catalogStore(env)
	steps := []struct {
		typ, processor, payload string
		project                 func(context.Context, catalog.EventRecord, time.Time) (catalog.ProjectResult, error)
	}{
		{catalog.EventCategorySnapshotV1, catalog.ProcessorCategoryProjectionV1, categoryPayload(ids["child"], "active", map[string]string{"ar": "فرع"}, []string{ids["root"]}, 1), d.ProjectCategory},
		{catalog.EventProductSnapshotV2, catalog.ProcessorProductProjectionV1, string(productRaw), d.ProjectProduct},
		{catalog.EventProductVariantSnapshotV1, catalog.ProcessorProductVariantProjectionV1, variantPayload(ids["variant"], ids["product"], "ML-CHAIN-V", true, false, "color=blue", nil, 1), d.ProjectProductVariant},
		{catalog.EventInventoryProductVariantSnapshotV1, catalog.ProcessorProductVariantInventoryProjectionV1, variantInventoryPayload(ids["variant"], ids["product"], "ML-CHAIN-V", 4, 1, true, nil), d.ProjectProductVariantInventory},
	}
	records := make([]catalog.EventRecord, len(steps))
	due := make([]time.Time, len(steps))
	for i, step := range steps {
		event := variantEventID(47100 + i)
		ingestCatalog(t, env, event, step.typ, "2026-09-20T10:00:00Z", step.payload)
		var ok bool
		records[i], ok, err = d.LoadCatalogEvent(context.Background(), event)
		if err != nil || !ok {
			t.Fatalf("load:%v %v", ok, err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		for attempt := 1; attempt <= 7; attempt++ {
			res, err := step.project(context.Background(), records[i], now)
			if err == nil || res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
				t.Fatalf("%s attempt%d result=%+v err=%v", step.typ, attempt, res, err)
			}
			now = catalogRetryDeadline(t, env, event, step.processor, now, attempt)
		}
		due[i] = now
	}
	rootEvent, typeEvent := variantEventID(47200), variantEventID(47201)
	ingestCatalog(t, env, rootEvent, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z", categoryPayload(ids["root"], "active", map[string]string{"ar": "قسم"}, nil, 1))
	ingestCatalog(t, env, typeEvent, catalog.EventProductTypeSnapshotV1, "2026-09-20T10:00:00Z", typePayload(ids["type"], "book", 1))
	for _, event := range []string{rootEvent, typeEvent} {
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatal(res)
		}
	}
	for i, step := range steps {
		res, err := step.project(context.Background(), records[i], due[i])
		if err != nil || res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("%s converge result=%+v err=%v", step.typ, res, err)
		}
	}
	var actualType, category string
	var stock, revision int64
	if err := env.pool.QueryRow(context.Background(), `SELECT product_type_id::text,top_category_id::text FROM catalog_products WHERE product_id=$1`, ids["product"]).Scan(&actualType, &category); err != nil || actualType != ids["type"] || category != ids["root"] {
		t.Fatalf("type=%s category=%s err=%v", actualType, category, err)
	}
	if err := env.pool.QueryRow(context.Background(), `SELECT stock_quantity,source_revision FROM catalog_product_variant_inventory WHERE variant_id=$1`, ids["variant"]).Scan(&stock, &revision); err != nil || stock != 4 || revision != 1 {
		t.Fatalf("stock=%d revision=%d err=%v", stock, revision, err)
	}
}

func TestCatalogSQLAbortRetriesUseDurableBoundedDeadlines(t *testing.T) {
	for _, state := range []string{"40001", "40P01", "23505", "42703"} {
		t.Run(state, func(t *testing.T) {
			env := openSaleEnv(t)
			_, product := seedCatalogProduct(t, env, 46000, "ML-SQL-RETRY")
			variant := catalogIDs(t, 46010, "v")["v"]
			variantEvent := variantEventID(46011)
			ingestCatalog(t, env, variantEvent, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z", variantPayload(variant, product, "ML-SQL-V", true, false, "color=blue", nil, 1))
			if res := projectCatalogOnce(t, env, variantEvent); res.Outcome != catalog.OutcomeProcessed {
				t.Fatal(res)
			}
			event := variantEventID(46012)
			ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:01:00Z", variantInventoryPayload(variant, product, "ML-SQL-V", 2, 1, true, nil))
			d := catalogStore(env)
			record, ok, err := d.LoadCatalogEvent(context.Background(), event)
			if err != nil || !ok {
				t.Fatalf("load: %v %v", ok, err)
			}
			var payloadBefore, hashBefore []byte
			if err := env.pool.QueryRow(context.Background(), `SELECT payload::text, payload_hash FROM sync_events WHERE event_id=$1`, event).Scan(&payloadBefore, &hashBefore); err != nil {
				t.Fatal(err)
			}
			// Sequence increments survive transaction rollback, giving an exact
			// failure count without timing or a process-local correctness lock.
			_, err = env.pool.Exec(context.Background(), fmt.Sprintf(`CREATE SEQUENCE retry_abort_count; CREATE FUNCTION retry_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF nextval('retry_abort_count') <= 7 THEN RAISE EXCEPTION 'synthetic private failure' USING ERRCODE='%s'; END IF; RETURN NEW; END $$; CREATE TRIGGER retry_abort BEFORE INSERT ON catalog_product_variant_inventory FOR EACH ROW EXECUTE FUNCTION retry_abort()`, state))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Microsecond)
			for attempt := 1; attempt <= 7; attempt++ {
				res, err := d.ProjectProductVariantInventory(context.Background(), record, now)
				if err == nil || res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrProjection {
					t.Fatalf("attempt=%d result=%+v err=%v", attempt, res, err)
				}
				want := catalog.Backoff(attempt - 1)
				if state == "40001" || state == "40P01" {
					want = 2 * time.Second
					if attempt == 1 {
						want = 500 * time.Millisecond
					} else if attempt == 2 {
						want = time.Second
					}
				}
				next := catalogRetryDeadline(t, env, event, catalog.ProcessorProductVariantInventoryProjectionV1, now, attempt, want)
				if next.Sub(now) != want {
					t.Fatalf("state=%s delay=%v want=%v", state, next.Sub(now), want)
				}
				now = next
				var diagnostic string
				if err := env.pool.QueryRow(context.Background(), `SELECT last_error_message FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, event, catalog.ProcessorProductVariantInventoryProjectionV1).Scan(&diagnostic); err != nil || diagnostic != "variant inventory upsert failed" {
					t.Fatalf("diagnostic=%q err=%v", diagnostic, err)
				}
				if n := saleCount(t, env.pool, "catalog_product_variant_inventory"); n != 0 {
					t.Fatalf("aborted upsert persisted%drows", n)
				}
			}
			expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			_, err = d.ProjectProductVariantInventory(expired, record, now)
			cancel()
			if err == nil {
				t.Fatal("expired context performed a projection")
			}
			var abortCalls int64
			if err := env.pool.QueryRow(context.Background(), `SELECT last_value FROM retry_abort_count`).Scan(&abortCalls); err != nil || abortCalls != 7 {
				t.Fatalf("expired context executedSQL: calls=%d err=%v", abortCalls, err)
			}
			res, err := d.ProjectProductVariantInventory(context.Background(), record, now)
			if err != nil || res.Outcome != catalog.OutcomeProcessed {
				t.Fatalf("converge result=%+v err=%v", res, err)
			}
			var calls, stock, revision int64
			if err := env.pool.QueryRow(context.Background(), `SELECT last_value FROM retry_abort_count`).Scan(&calls); err != nil || calls != 8 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if err := env.pool.QueryRow(context.Background(), `SELECT stock_quantity, source_revision FROM catalog_product_variant_inventory WHERE variant_id=$1`, variant).Scan(&stock, &revision); err != nil || stock != 2 || revision != 1 {
				t.Fatalf("stock=%d revision=%d err=%v", stock, revision, err)
			}
			var payloadAfter, hashAfter []byte
			if err := env.pool.QueryRow(context.Background(), `SELECT payload::text, payload_hash FROM sync_events WHERE event_id=$1`, event).Scan(&payloadAfter, &hashAfter); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(payloadBefore, payloadAfter) || !bytes.Equal(hashBefore, hashAfter) {
				t.Fatal("durable retry changed inbox payload/hash")
			}
		})
	}
}
