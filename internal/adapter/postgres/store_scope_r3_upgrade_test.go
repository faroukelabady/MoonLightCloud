package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
)

// This fixture starts at the actual old schema and never calls a canonical
// projector before seeding raw rows. Every fixture write checks its row count.
func testR3RealUpgrade(t *testing.T) {
	ctx := context.Background()
	url := testutil.Raw(t)
	conn, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = migrate.UpTo(ctx, conn, 24); err != nil {
		t.Fatal(err)
	}
	pool, authSvc := openTestRepoOnURL(t, url)
	f := &scopeFixture{pool: pool}
	devices := NewDevices(pool, 5*time.Second)
	f.syncSvc = isync.NewService(devices, clock.System{})
	for i, name := range []string{"old A", "old B", "legacy"} {
		p, e := authSvc.Create(ctx, name)
		if e != nil {
			t.Fatal(e)
		}
		switch i {
		case 0:
			f.devA, f.credA = p.Device.ID, p.Credential.ID
		case 1:
			f.devB, f.credB = p.Device.ID, p.Credential.ID
		case 2:
			f.devC, f.credC = p.Device.ID, p.Credential.ID
		}
	}
	for _, b := range []struct{ dev, id string }{{f.devA, scopeStoreA}, {f.devB, scopeStoreB}} {
		if _, err = devices.RegisterStore(ctx, b.dev, store.RegistrationRequest{StoreID: b.id, DisplayName: b.id, Timezone: "Africa/Cairo"}); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(want int64, q string, args ...any) {
		t.Helper()
		r, e := pool.Exec(ctx, q, args...)
		if e != nil || r.RowsAffected() != want {
			t.Fatalf("fixture rows=%d want=%d err=%v query=%s", r.RowsAffected(), want, e, q)
		}
	}
	root, child, tag, product := sharedCatIslamic, "a9000000-0000-4000-8000-000000000031", sharedTagGold, "a9000000-0000-4000-8000-000000000032"
	events := []struct{ id, typ, payload, processor string }{
		{r2EventID(800), catalog.EventCategorySnapshotV1, categoryPayload(root, "active", map[string]string{"ar": "قديم", "en": "old root"}, nil, 1), catalog.ProcessorCategoryProjectionV1},
		{r2EventID(801), catalog.EventCategorySnapshotV1, categoryPayload(child, "active", map[string]string{"ar": "فرع", "en": "old child"}, []string{root}, 1), catalog.ProcessorCategoryProjectionV1},
		{r2EventID(802), catalog.EventTagSnapshotV1, tagPayload(tag, "gold", true, map[string]string{"ar": "ذهب", "en": "old gold"}, 1), catalog.ProcessorTagProjectionV1},
		{r2EventID(803), catalog.EventProductSnapshotV1, productPayload(product, "OLD-SKU", "old product", root, []string{child}, []string{tag}, 1), catalog.ProcessorProductProjectionV1},
	}
	for _, e := range events {
		f.ingest(t, f.devA, f.credA, e.id, e.typ, e.payload)
		exec(1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, e.id, e.processor)
	}
	for _, e := range events[:2] {
		exec(1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
 SELECT (payload->>'category_id')::uuid,'active',CASE WHEN event_id=$1::uuid THEN $2::text END,$3,1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, e.id, map[string]string{root: "قديم", child: "فرع"}[map[string]string{events[0].id: root, events[1].id: child}[e.id]], map[string]string{events[0].id: "old root", events[1].id: "old child"}[e.id])
	}
	exec(1, `INSERT INTO catalog_category_edges(parent_id,child_id,position) VALUES($1,$2,0)`, root, child)
	exec(1, `INSERT INTO catalog_tags(tag_id,slug,is_active,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
 SELECT (payload->>'tag_id')::uuid,'gold',true,'ذهب','old gold',1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, events[2].id)
	exec(1, `INSERT INTO catalog_products(product_id,sku,name,top_category_id,width_cm,height_cm,is_active,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
 SELECT (payload->>'product_id')::uuid,'OLD-SKU','old product',$2,70,100,true,1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, events[3].id, root)
	exec(1, `INSERT INTO catalog_product_subcategories VALUES($1,$2,0)`, product, child)
	exec(1, `INSERT INTO catalog_product_tags VALUES($1,$2)`, product, tag)
	exec(1, `INSERT INTO catalog_product_translations(product_id,locale,name) VALUES($1,'ar','old product')`, product)
	exec(2, `INSERT INTO catalog_product_prices VALUES($1,'EGP',65000,40000),($1,'USD',1300,NULL)`, product)

	// Unbound history retains the raw namespace. Its current Product and
	// default edge must not acquire a fabricated Store during recovery.
	legacyChild := "00000000-0000-0000-0000-000000000201"
	legacyProduct := "a9000000-0000-4000-8000-000000000033"
	f.ingest(t, f.devC, f.credC, r2EventID(806), catalog.EventCategorySnapshotV1, categoryPayload(legacyChild, "active", map[string]string{"ar": "قديم", "en": "legacy"}, []string{root}, 1))
	f.ingest(t, f.devC, f.credC, r2EventID(807), catalog.EventProductSnapshotV1, productPayload(legacyProduct, "LEGACY-SKU", "legacy product", root, []string{legacyChild}, []string{tag}, 1))
	exec(1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at)
 SELECT $2,'active','قديم','legacy',1,event_id,device_id,payload_hash,received_at FROM sync_events WHERE event_id=$1`, r2EventID(806), legacyChild)
	exec(1, `INSERT INTO catalog_category_edges VALUES($1,$2,0)`, root, legacyChild)
	exec(1, `INSERT INTO catalog_products(product_id,sku,name,top_category_id,width_cm,height_cm,is_active,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at)
 SELECT $2,'LEGACY-SKU','legacy product',$3,70,100,true,1,event_id,device_id,payload_hash,received_at FROM sync_events WHERE event_id=$1`, r2EventID(807), legacyProduct, root)
	exec(1, `INSERT INTO catalog_product_subcategories VALUES($1,$2,0)`, legacyProduct, legacyChild)
	exec(1, `INSERT INTO catalog_product_tags VALUES($1,$2)`, legacyProduct, tag)
	exec(1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, r2EventID(806), catalog.ProcessorCategoryProjectionV1)
	exec(1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, r2EventID(807), catalog.ProcessorProductProjectionV1)
	var legacyBefore string
	if err = pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM catalog_products p WHERE product_id=$1`, legacyProduct).Scan(&legacyBefore); err != nil {
		t.Fatal(err)
	}
	// A real pre-fix foreign default block must be distinguishable from
	// genuine same-Store equal-revision conflicts with the same code.
	foreign := r2EventID(804)
	f.ingest(t, f.devB, f.credB, foreign, catalog.EventTagSnapshotV1, tagPayload(tag, "gold", true, map[string]string{"en": "B old gold"}, 1))
	exec(1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,last_error_code,last_error_message) VALUES($1,$2,'blocked',1,$3,'tag owned by another store')`, foreign, catalog.ProcessorTagProjectionV1, ErrStoreScopeConflict)
	genuine := r2EventID(805)
	f.ingest(t, f.devA, f.credA, genuine, catalog.EventTagSnapshotV1, tagPayload(tag, "gold", true, map[string]string{"en": "genuine conflict"}, 1))
	exec(1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,last_error_code,last_error_message) VALUES($1,$2,'blocked',1,$3,'equal revision with conflicting state')`, genuine, catalog.ProcessorTagProjectionV1, ErrCatalogRevisionConflict)
	// Prove the raw identity, owner, revision, processing and references
	// exist before upgrade, rather than silently updating an absent row.
	var owner, status string
	var revision int64
	var rawRefs int
	if err = pool.QueryRow(ctx, `SELECT store_id::text,source_revision FROM catalog_tags WHERE tag_id=$1`, tag).Scan(&owner, &revision); err != nil || owner != scopeStoreA || revision != 1 {
		t.Fatalf("old raw tag %s %d: %v", owner, revision, err)
	}
	if err = pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, events[3].id, events[3].processor).Scan(&status); err != nil || status != "processed" {
		t.Fatal("old processed fixture missing", err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM catalog_products p JOIN catalog_product_tags t USING(product_id) WHERE p.top_category_id=$1 AND t.tag_id=$2`, root, tag).Scan(&rawRefs); err != nil || rawRefs != 2 {
		t.Fatal("raw references missing", err)
	}

	// Real historical projections exist at schema 24 before migration or
	// recovery. Their IDs, labels, capture state and exact money remain
	// untouched even when current catalog identities change.
	var historicalPayload map[string]any
	if err = json.Unmarshal([]byte(v2Fixture(t, nil)), &historicalPayload); err != nil {
		t.Fatal(err)
	}
	for _, line := range historicalPayload["lines"].([]any) {
		line.(map[string]any)["tags"] = []any{map[string]any{"tag_id": tag, "slug": "gold", "name_ar": "ذهب قديم", "name_en": "Old Gold"}}
	}
	encoded, err := json.Marshal(historicalPayload)
	if err != nil {
		t.Fatal(err)
	}
	f.ingest(t, f.devA, f.credA, r2EventID(808), sale.EventSaleFinalizedV2, string(encoded))
	if result := f.projectSaleV2(t, r2EventID(808)); result.Outcome != sale.OutcomeProcessed {
		t.Fatalf("old Sale projection: %+v", result)
	}
	f.ingest(t, f.devA, f.credA, r2EventID(809), returnrefund.EventReturnRefundFinalizedV1, alignedUSDReturnPayload(t, "a9000000-0000-4000-8000-000000000034", "OLD-RETURN"))
	if result := f.projectReturn(t, r2EventID(809)); result.Outcome != returnrefund.OutcomeProcessed {
		t.Fatalf("old Return projection: %+v", result)
	}
	historyBefore := r3HistoricalState(t, f)
	for _, entity := range []struct{ table, key, id string }{{"catalog_categories", "category_id", root}, {"catalog_categories", "category_id", child}, {"catalog_products", "product_id", product}} {
		if err = pool.QueryRow(ctx, `SELECT store_id::text,source_revision FROM `+entity.table+` WHERE `+entity.key+`=$1`, entity.id).Scan(&owner, &revision); err != nil || owner != scopeStoreA || revision != 1 {
			t.Fatalf("raw fixture %s owner=%s rev=%d: %v", entity.id, owner, revision, err)
		}
	}
	for _, event := range events {
		if err = pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, event.id, event.processor).Scan(&status); err != nil || status != "processed" {
			t.Fatalf("old source %s: %s %v", event.id, status, err)
		}
	}
	var payloadBefore string
	if err = pool.QueryRow(ctx, `SELECT string_agg(event_id::text||payload::text||encode(payload_hash,'hex'),'|' ORDER BY event_id) FROM sync_events`).Scan(&payloadBefore); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	// Recovery is atomic even when a later processing reset fails.
	exec(0, `CREATE FUNCTION r3_recovery_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_id='`+events[1].id+`'::uuid THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$`)
	exec(0, `CREATE TRIGGER r3_recovery_fail BEFORE UPDATE ON sync_event_processing FOR EACH ROW EXECUTE FUNCTION r3_recovery_fail()`)
	if _, err = devices.RecoverDefaultCatalogBlocked(ctx); err == nil {
		t.Fatal("fault must reject recovery")
	}
	var pending int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM sync_event_processing WHERE status='pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("partial recovery commit %d: %v", pending, err)
	}
	exec(0, `DROP TRIGGER r3_recovery_fail ON sync_event_processing`)
	exec(0, `DROP FUNCTION r3_recovery_fail()`)
	n, err := devices.RecoverDefaultCatalogBlocked(ctx)
	if err != nil || n != 5 {
		t.Fatalf("recovery count=%d: %v", n, err)
	}
	// Reopen the repository/pool after the durable reset, before processing.
	restarted, _ := openTestRepoOnURL(t, url)
	f.pool = restarted
	devices = NewDevices(restarted, 5*time.Second)
	r3Converge(t, f)
	canonicalRoot, canonicalTag := scopedCatID(t, root, scopeStoreA), scopedTagID(t, tag, scopeStoreA)
	var top, tagRef, parent string
	if err = restarted.QueryRow(ctx, `SELECT top_category_id::text FROM catalog_products WHERE product_id=$1`, product).Scan(&top); err != nil || top != canonicalRoot {
		t.Fatalf("top=%s: %v", top, err)
	}
	if err = restarted.QueryRow(ctx, `SELECT tag_id::text FROM catalog_product_tags WHERE product_id=$1`, product).Scan(&tagRef); err != nil || tagRef != canonicalTag {
		t.Fatalf("tag=%s: %v", tagRef, err)
	}
	if err = restarted.QueryRow(ctx, `SELECT parent_id::text FROM catalog_category_edges WHERE child_id=$1`, child).Scan(&parent); err != nil || parent != canonicalRoot {
		t.Fatalf("parent=%s: %v", parent, err)
	}
	if n, err = devices.RecoverDefaultCatalogBlocked(ctx); err != nil || n != 0 {
		t.Fatalf("repeat recovery=%d: %v", n, err)
	}

	// Partial transition: defaults already canonical, but processed custom
	// edges and Product references still raw. Replaying the exact source
	// revision must repair representation, not create a semantic conflict.
	exec(1, `UPDATE catalog_products SET top_category_id=$2 WHERE product_id=$1`, product, root)
	exec(1, `UPDATE catalog_product_tags SET tag_id=$2 WHERE product_id=$1`, product, tag)
	exec(1, `UPDATE catalog_category_edges SET parent_id=$2 WHERE child_id=$1`, child, root)
	if n, err = devices.RecoverDefaultCatalogBlocked(ctx); err != nil || n != 2 {
		t.Fatalf("partial recovery=%d: %v", n, err)
	}
	r3Converge(t, f)
	if n, err = devices.RecoverDefaultCatalogBlocked(ctx); err != nil || n != 0 {
		t.Fatalf("partial repeat recovery=%d: %v", n, err)
	}
	if err = restarted.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, genuine, catalog.ProcessorTagProjectionV1).Scan(&status); err != nil || status != "blocked" {
		t.Fatalf("genuine conflict reset: %s %v", status, err)
	}
	if err = restarted.QueryRow(ctx, `SELECT top_category_id::text FROM catalog_products WHERE product_id=$1`, product).Scan(&top); err != nil || top != canonicalRoot {
		t.Fatalf("partial top=%s: %v", top, err)
	}
	var payloadAfter string
	if err = restarted.QueryRow(ctx, `SELECT string_agg(event_id::text||payload::text||encode(payload_hash,'hex'),'|' ORDER BY event_id) FROM sync_events`).Scan(&payloadAfter); err != nil || payloadBefore != payloadAfter {
		t.Fatal("payload/hash rewritten", err)
	}

	var legacyAfter string
	if err = restarted.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM catalog_products p WHERE product_id=$1`, legacyProduct).Scan(&legacyAfter); err != nil || legacyBefore != legacyAfter {
		t.Fatalf("legacy current state changed: %v", err)
	}
	if err = restarted.QueryRow(ctx, `SELECT count(*) FROM catalog_category_edges WHERE parent_id=$1 AND child_id=$2`, root, legacyChild).Scan(&rawRefs); err != nil || rawRefs != 1 {
		t.Fatalf("legacy edge changed: %v", err)
	}

	if after := r3HistoricalState(t, f); after != historyBefore {
		t.Fatal("migration/recovery rewrote historical Sale/Return state")
	}
	var historicalTag string
	if err = restarted.QueryRow(ctx, `SELECT tag_id::text FROM sale_item_tag_snapshots LIMIT 1`).Scan(&historicalTag); err != nil || historicalTag != tag {
		t.Fatalf("historical tag remapped: %s %v", historicalTag, err)
	}
	// Further traffic uses the existing Store slug without a collision;
	// another Store obtains its own independent history from its own event.
	for i, b := range []struct{ dev, cred string }{{f.devA, f.credA}, {f.devB, f.credB}} {
		e := r2EventID(810 + i)
		f.ingest(t, b.dev, b.cred, e, catalog.EventTagSnapshotV1, tagPayload(tag, "gold", true, map[string]string{"en": fmt.Sprintf("new %d", i)}, 2))
		requireOutcome(t, r3Project(t, f, e, catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	}
}

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
			events, err := d.PendingCatalogEvents(ctx, p.processor, p.typ, 100)
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
		if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]')::text FROM `+table+` t`).Scan(&raw); err != nil {
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
