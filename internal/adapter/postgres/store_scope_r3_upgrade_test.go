package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
)

func r3Converge(t *testing.T, f *scopeFixture) {
	t.Helper()
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	for attempt := 0; attempt < 30; attempt++ {
		if _, err := f.pool.Exec(ctx, `UPDATE sync_event_processing SET next_attempt_at=now()-interval '1 second' WHERE status='retry' AND processor LIKE 'catalog_%'`); err != nil {
			t.Fatal(err)
		}
		remaining := 0
		for _, p := range []struct{ processor, typ string }{{catalog.ProcessorCategoryProjectionV1, catalog.EventCategorySnapshotV1}, {catalog.ProcessorTagProjectionV1, catalog.EventTagSnapshotV1}, {catalog.ProcessorProductProjectionV1, catalog.EventProductSnapshotV1}} {
			events, err := d.PendingCatalogEvents(ctx, p.processor, p.typ, 100, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			remaining += len(events)
			for _, e := range events {
				res := r3Project(t, f, e, p.typ)
				if res.Outcome == catalog.OutcomeBlocked {
					t.Fatalf("convergence blocked %s: %+v", e, res)
				}
			}
		}
		if remaining == 0 {
			return
		}
	}
	t.Fatal("catalog did not reach terminal convergence")
}

func TestR3_RecoveryBatchBound(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	_, authSvc := openTestRepoOnURL(t, f.pool.Config().ConnString())
	d := NewDevices(f.pool, 5*time.Second)
	rawIDs := []string{}
	for id := range sharedTagIDs {
		rawIDs = append(rawIDs, id)
	}
	sort.Strings(rawIDs)
	if len(rawIDs) < 5 {
		t.Fatal("expected default tag set")
	}
	rawIDs = rawIDs[:5]
	for i := 0; i < 21; i++ {
		p, err := authSvc.Create(ctx, fmt.Sprintf("old batch %d", i))
		if err != nil {
			t.Fatal(err)
		}
		storeID := fmt.Sprintf("a9000000-0000-4000-8000-%012d", 1000+i)
		if _, err = d.RegisterStore(ctx, p.Device.ID, store.RegistrationRequest{StoreID: storeID, DisplayName: "old batch", Timezone: "Africa/Cairo"}); err != nil {
			t.Fatal(err)
		}
		for j, id := range rawIDs {
			event := r2EventID(900 + i*5 + j)
			f.ingest(t, p.Device.ID, p.Credential.ID, event, catalog.EventTagSnapshotV1, tagPayload(id, fmt.Sprintf("tag-%d", j), true, map[string]string{"en": "historical store tag"}, 1))
			res, err := f.pool.Exec(ctx, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, event, catalog.ProcessorTagProjectionV1)
			if err != nil || res.RowsAffected() != 1 {
				t.Fatal("processed old history", err)
			}
		}
	}
	n, err := d.RecoverDefaultCatalogBlocked(ctx)
	if err != nil || n != 100 {
		t.Fatalf("first batch=%d %v", n, err)
	}
	r3Converge(t, f)
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_tags WHERE default_algorithm=1`).Scan(&count); err != nil || count != 100 {
		t.Fatalf("first batch state=%d %v", count, err)
	}
	n, err = d.RecoverDefaultCatalogBlocked(ctx)
	if err != nil || n != 5 {
		t.Fatalf("last batch=%d %v", n, err)
	}
	r3Converge(t, f)
	n, err = d.RecoverDefaultCatalogBlocked(ctx)
	if err != nil || n != 0 {
		t.Fatalf("converged batch=%d %v", n, err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_tags WHERE default_algorithm=1`).Scan(&count); err != nil || count != 105 {
		t.Fatalf("full batch state=%d %v", count, err)
	}
}

// Concrete foreign raw provenance is necessary but not sufficient: a
// conflicting equal revision with an already processed own-Store source
// remains a genuine conflict even after a foreign raw row replaces it.
func TestR3_RecoveryPreservesEqualConflictWithForeignRaw(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	first, conflict, foreign := r2EventID(1100), r2EventID(1101), r2EventID(1102)
	f.ingest(t, f.devA, f.credA, first, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "A accepted"}, 1))
	f.ingest(t, f.devA, f.credA, conflict, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "A contradiction"}, 1))
	f.ingest(t, f.devB, f.credB, foreign, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "B accepted"}, 1))
	exec := func(want int64, q string, args ...any) {
		t.Helper()
		r, err := f.pool.Exec(ctx, q, args...)
		if err != nil || r.RowsAffected() != want {
			t.Fatalf("fixture rows=%d want=%d: %v", r.RowsAffected(), want, err)
		}
	}
	exec(2, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$3,'processed',1,now()),($2,$3,'processed',1,now())`, first, foreign, catalog.ProcessorTagProjectionV1)
	exec(1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,last_error_code,last_error_message) VALUES($1,$2,'blocked',1,$3,'equal revision with conflicting state')`, conflict, catalog.ProcessorTagProjectionV1, ErrCatalogRevisionConflict)
	exec(1, `INSERT INTO catalog_tags(tag_id,slug,is_active,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) SELECT $2,'gold',true,'B accepted',1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, foreign, sharedTagGold)
	n, err := d.RecoverDefaultCatalogBlocked(ctx)
	if err != nil || n != 2 {
		t.Fatalf("recovery sources=%d %v", n, err)
	}
	r3Converge(t, f)
	var status, name string
	if err = f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, conflict, catalog.ProcessorTagProjectionV1).Scan(&status); err != nil || status != "blocked" {
		t.Fatalf("equal conflict reset=%s %v", status, err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT name_en FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreA)).Scan(&name); err != nil || name != "A accepted" {
		t.Fatalf("wrong A basis=%s %v", name, err)
	}
}

func r3HistoricalState(t *testing.T, f *scopeFixture) string {
	t.Helper()
	state := map[string]string{}
	for _, table := range []string{"sales_projection", "sale_lines_projection", "sale_line_classifications_projection", "sale_item_tag_snapshots", "return_refund_projection", "return_refund_lines_projection", "return_refund_payments_projection"} {
		var raw string
		// 00036/00038 append nullable Variant and ProductType snapshot columns
		// (Phase 17-R0): they are schema additions, not rewrites, so both
		// sides strip them and compare every pre-existing value exactly.
		if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) - 'variant_sku' - 'variant_attributes' - 'variant_price_egp_cents' - 'variant_price_usd_cents' - 'product_type_id' - 'product_type_code' - 'product_type_name_ar' - 'product_type_name_en' ORDER BY to_jsonb(t)::text),'[]')::text FROM `+table+` t`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		state[table] = raw
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
