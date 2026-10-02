package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

func r4Exec(t *testing.T, f *scopeFixture, want int64, query string, args ...any) {
	t.Helper()
	result, err := f.pool.Exec(context.Background(), query, args...)
	if err != nil || result.RowsAffected() != want {
		t.Fatalf("fixture affected=%d want=%d: %v", result.RowsAffected(), want, err)
	}
}

func r4Raw(t *testing.T, f *scopeFixture, category bool) string {
	t.Helper()
	return r4RawEvent(t, f, category, r2EventID(9400))
}

func r4RawEvent(t *testing.T, f *scopeFixture, category bool, event string) string {
	t.Helper()
	id, typ, proc := sharedTagGold, catalog.EventTagSnapshotV1, catalog.ProcessorTagProjectionV1
	payload := tagPayload(id, "gold", true, map[string]string{"ar": "ذهب", "en": "Gold"}, 1)
	if category {
		id, typ, proc = sharedCatIslamic, catalog.EventCategorySnapshotV1, catalog.ProcessorCategoryProjectionV1
		payload = categoryPayload(id, "active", map[string]string{"ar": "إسلامي", "en": "Islamic"}, nil, 1)
	}
	f.ingest(t, f.devA, f.credA, event, typ, payload)
	r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, event, proc)
	if category {
		r4Exec(t, f, 1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) SELECT $2,'active','إسلامي','Islamic',1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, event, id)
	} else {
		r4Exec(t, f, 1, `INSERT INTO catalog_tags(tag_id,slug,is_active,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) SELECT $2,'gold',true,'ذهب','Gold',1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, event, id)
	}
	return event
}

func r4Payloads(t *testing.T, f *scopeFixture) string {
	t.Helper()
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT jsonb_agg(jsonb_build_array(event_id,payload,payload_hash) ORDER BY event_id)::text FROM sync_events`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestR4_R2SlugCollision(t *testing.T) {
	for _, mode := range []string{"raw", "canonical", "newer", "multiple"} {
		t.Run(mode, func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			source := r4Raw(t, f, false)
			if mode != "raw" {
				requireOutcome(t, r4ProjectRearm(t, f, source, catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
			}
			last := int64(2)
			if mode == "multiple" {
				last = 3
			}
			for rev := int64(2); rev <= last; rev++ {
				e := r2EventID(9400 + int(rev))
				f.ingest(t, f.devA, f.credA, e, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"ar": "ذهب جديد", "en": fmt.Sprintf("Gold %d", rev)}, rev))
				r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,last_error_code,last_error_message) VALUES($1,$2,'blocked',1,'CATALOG_REVISION_CONFLICT','tag identity collision')`, e, catalog.ProcessorTagProjectionV1)
			}
			if mode == "newer" {
				e := r2EventID(9410)
				f.ingest(t, f.devA, f.credA, e, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"ar": "أحدث", "en": "Newest"}, 4))
				requireOutcome(t, r3Project(t, f, e, catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
			}
			before := r4Payloads(t, f)
			d := NewDevices(f.pool, 5*time.Second)
			n, err := d.RecoverDefaultCatalogBlocked(ctx)
			if err != nil || n == 0 {
				t.Fatalf("obsolete collision not queued: %d %v", n, err)
			}
			r3Converge(t, f)
			n, err = d.RecoverDefaultCatalogBlocked(ctx)
			if err != nil || n != 0 {
				t.Fatalf("final recovery=%d %v", n, err)
			}
			var rev int64
			var name string
			if err = f.pool.QueryRow(ctx, `SELECT source_revision,name_en FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreA)).Scan(&rev, &name); err != nil {
				t.Fatal(err)
			}
			want := last
			if mode == "newer" {
				want = 4
			}
			if rev != want {
				t.Fatalf("revision=%d want=%d", rev, want)
			}
			for v := int64(2); v <= last; v++ {
				var status string
				if err = f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, r2EventID(9400+int(v)), catalog.ProcessorTagProjectionV1).Scan(&status); err != nil || status != "processed" {
					t.Fatalf("obsolete revision %d: %s %v", v, status, err)
				}
			}
			if before != r4Payloads(t, f) {
				t.Fatal("durable payload/hash changed")
			}
			t.Logf("revisions processed, canonical=%d %s, final recovery=0", rev, name)
		})
	}
}

func r4ProjectRearm(t *testing.T, f *scopeFixture, event, typ string) catalog.ProjectResult {
	t.Helper()
	proc := catalog.ProcessorTagProjectionV1
	if typ == catalog.EventCategorySnapshotV1 {
		proc = catalog.ProcessorCategoryProjectionV1
	}
	r4Exec(t, f, 1, `UPDATE sync_event_processing SET status='pending',processed_at=NULL WHERE event_id=$1 AND processor=$2`, event, proc)
	return r3Project(t, f, event, typ)
}

func TestR4_CanonicalRetirement(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(fmt.Sprintf("newer=%v", newer), func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			source := r4Raw(t, f, true)
			// Seed actual pre-R4 coexistence, not a projector-generated retired row.
			revision := int64(1)
			e := source
			en, ar := "Islamic", "إسلامي"
			if newer {
				revision = 3
				e = r2EventID(9420)
				en, ar = "New Islamic", "إسلامي جديد"
				f.ingest(t, f.devA, f.credA, e, catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", map[string]string{"ar": ar, "en": en}, nil, revision))
				r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, e, catalog.ProcessorCategoryProjectionV1)
			}
			canonical := scopedCatID(t, sharedCatIslamic, scopeStoreA)
			r4Exec(t, f, 1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id,default_algorithm) SELECT $2,'active',$3,$4,$5,event_id,device_id,payload_hash,received_at,store_id,1 FROM sync_events WHERE event_id=$1`, e, canonical, ar, en, revision)
			before := r4Payloads(t, f)
			d := NewDevices(f.pool, 5*time.Second)
			n, err := d.RecoverDefaultCatalogBlocked(ctx)
			if err != nil || n != 1 {
				t.Fatalf("initial recovery=%d %v", n, err)
			}
			r3Converge(t, f)
			var retired bool
			if err = f.pool.QueryRow(ctx, `SELECT store_id IS NULL AND default_algorithm=2 FROM catalog_categories WHERE category_id=$1`, sharedCatIslamic).Scan(&retired); err != nil || !retired {
				t.Fatalf("raw not retired: %v %v", retired, err)
			}
			n, err = d.RecoverDefaultCatalogBlocked(ctx)
			if err != nil || n != 0 {
				t.Fatalf("recovery repeats=%d %v", n, err)
			}
			var got int64
			var name string
			if err = f.pool.QueryRow(ctx, `SELECT source_revision,name_en FROM catalog_categories WHERE category_id=$1`, canonical).Scan(&got, &name); err != nil || got != revision || name != en {
				t.Fatalf("canonical regressed: %d %s %v", got, name, err)
			}
			if before != r4Payloads(t, f) {
				t.Fatal("payload/hash changed")
			}
			t.Logf("retired raw, preserved canonical revision=%d, recovery=0", got)
		})
	}
}

func TestR4_GenuineConflicts(t *testing.T) {
	for _, mode := range []string{"equal", "slug", "custom", "validation", "foreign-source"} {
		t.Run(mode, func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			source := r4Raw(t, f, false)
			id, revision := sharedTagGold, int64(2)
			if mode == "equal" {
				revision = 1
			}
			if mode == "custom" {
				id = "a9000000-0000-4000-8000-000000009500"
			}
			if mode == "foreign-source" {
				r4Exec(t, f, 1, `UPDATE sync_events SET store_id=$2 WHERE event_id=$1`, source, scopeStoreB)
			}
			if mode == "slug" {
				other := r2EventID(9501)
				custom := "a9000000-0000-4000-8000-000000009501"
				f.ingest(t, f.devA, f.credA, other, catalog.EventTagSnapshotV1, tagPayload(custom, "gold", true, map[string]string{"ar": "أخر", "en": "Other"}, 1))
				// Reconstruct a genuine canonical/custom claimant after obsolete raw retirement.
				r4Exec(t, f, 1, `UPDATE catalog_tags SET store_id=NULL,default_algorithm=2 WHERE tag_id=$1`, sharedTagGold)
				requireOutcome(t, r3Project(t, f, other, catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
			}
			e := r2EventID(9500)
			f.ingest(t, f.devA, f.credA, e, catalog.EventTagSnapshotV1, tagPayload(id, "gold", true, map[string]string{"ar": "متناقض", "en": "Contradiction"}, revision))
			code := ErrCatalogRevisionConflict
			if mode == "validation" {
				code = ErrValidation
			}
			r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,last_error_code,last_error_message) VALUES($1,$2,'blocked',1,$3,'tag identity collision')`, e, catalog.ProcessorTagProjectionV1, code)
			before := r4Payloads(t, f)
			d := NewDevices(f.pool, 5*time.Second)
			if _, err := d.RecoverDefaultCatalogBlocked(ctx); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, e, catalog.ProcessorTagProjectionV1).Scan(&status); err != nil || status != "blocked" {
				t.Fatalf("genuine conflict reset: %s %v", status, err)
			}
			if before != r4Payloads(t, f) {
				t.Fatal("recovery changed durable event")
			}
		})
	}
}

func TestR4_RetirementConflictAndInterruption(t *testing.T) {
	for _, mode := range []string{"conflict", "interruption", "stale"} {
		t.Run(mode, func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			source := r4Raw(t, f, true)
			canonical := scopedCatID(t, sharedCatIslamic, scopeStoreA)
			en := "Islamic"
			revision := int64(1)
			e := source
			if mode == "conflict" {
				en = "Contradiction"
			}
			if mode == "stale" {
				revision = 3
				e = r2EventID(9600)
				f.ingest(t, f.devA, f.credA, e, catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", map[string]string{"ar": "إسلامي", "en": en}, nil, revision))
				r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, e, catalog.ProcessorCategoryProjectionV1)
			}
			r4Exec(t, f, 1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id,default_algorithm) SELECT $2,'active','إسلامي',$3,$4,event_id,device_id,payload_hash,received_at,store_id,1 FROM sync_events WHERE event_id=$1`, e, canonical, en, revision)
			if mode == "interruption" {
				r4Exec(t, f, 0, `CREATE FUNCTION r4_retire_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.default_algorithm=2 THEN RAISE EXCEPTION 'injected retirement failure'; END IF; RETURN NEW; END $$`)
				r4Exec(t, f, 0, `CREATE TRIGGER r4_retire_fail BEFORE UPDATE ON catalog_categories FOR EACH ROW EXECUTE FUNCTION r4_retire_fail()`)
			}
			before := r4Payloads(t, f)
			result := r4ProjectRearm(t, f, source, catalog.EventCategorySnapshotV1)
			if mode == "conflict" {
				requireOutcome(t, result, catalog.OutcomeBlocked, ErrCatalogRevisionConflict)
			} else if mode == "interruption" {
				requireOutcome(t, result, catalog.OutcomeRetryable, ErrProjection)
			} else {
				requireOutcome(t, result, catalog.OutcomeProcessed, "")
			}
			var algorithm int
			if err := f.pool.QueryRow(ctx, `SELECT default_algorithm FROM catalog_categories WHERE category_id=$1`, sharedCatIslamic).Scan(&algorithm); err != nil {
				t.Fatal(err)
			}
			if mode != "stale" && algorithm != 0 {
				t.Fatal("failed/contradictory retirement committed")
			}
			if mode == "interruption" {
				r4Exec(t, f, 0, `DROP TRIGGER r4_retire_fail ON catalog_categories`)
				reopened, _ := openTestRepoOnURL(t, f.pool.Config().ConnString())
				f.pool = reopened
				r3Converge(t, f)
			}
			if mode != "conflict" {
				var retired bool
				if err := f.pool.QueryRow(ctx, `SELECT store_id IS NULL AND default_algorithm=2 FROM catalog_categories WHERE category_id=$1`, sharedCatIslamic).Scan(&retired); err != nil || !retired {
					t.Fatalf("retry retirement %v %v", retired, err)
				}
				n, err := NewDevices(f.pool, 5*time.Second).RecoverDefaultCatalogBlocked(ctx)
				if err != nil || n != 0 {
					t.Fatalf("final=%d %v", n, err)
				}
			}
			if before != r4Payloads(t, f) {
				t.Fatal("event changed")
			}
		})
	}
}

func TestR4_RetirementRacingProjection(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		f := openScopeFixture(t)
		ctx := context.Background()
		source := r4Raw(t, f, true)
		canonical := scopedCatID(t, sharedCatIslamic, scopeStoreA)
		r4Exec(t, f, 1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id,default_algorithm) SELECT $2,'active','إسلامي','Islamic',1,event_id,device_id,payload_hash,received_at,store_id,1 FROM sync_events WHERE event_id=$1`, source, canonical)
		n, err := NewDevices(f.pool, 5*time.Second).RecoverDefaultCatalogBlocked(ctx)
		if err != nil || n != 1 {
			t.Fatalf("queue=%d %v", n, err)
		}
		newer := r2EventID(9700)
		f.ingest(t, f.devA, f.credA, newer, catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", map[string]string{"ar": "جديد", "en": "New"}, nil, 2))
		var wg sync.WaitGroup
		for _, e := range []string{source, newer} {
			wg.Add(1)
			go func(id string) { defer wg.Done(); r3Project(t, f, id, catalog.EventCategorySnapshotV1) }(e)
		}
		wg.Wait()
		r3Converge(t, f)
		var rev int64
		var retired bool
		if err = f.pool.QueryRow(ctx, `SELECT source_revision FROM catalog_categories WHERE category_id=$1`, canonical).Scan(&rev); err != nil || rev != 2 {
			t.Fatalf("race canonical=%d %v", rev, err)
		}
		if err = f.pool.QueryRow(ctx, `SELECT store_id IS NULL AND default_algorithm=2 FROM catalog_categories WHERE category_id=$1`, sharedCatIslamic).Scan(&retired); err != nil || !retired {
			t.Fatal("race retirement incomplete", err)
		}
		n, err = NewDevices(f.pool, 5*time.Second).RecoverDefaultCatalogBlocked(ctx)
		if err != nil || n != 0 {
			t.Fatalf("race final=%d %v", n, err)
		}
	}
}

func TestR4_MixedBoundedRecovery(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	r4Raw(t, f, false)
	categorySource := r4RawEvent(t, f, true, r2EventID(9800))
	canonical := scopedCatID(t, sharedCatIslamic, scopeStoreA)
	r4Exec(t, f, 1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id,default_algorithm) SELECT $2,'active','إسلامي','Islamic',1,event_id,device_id,payload_hash,received_at,store_id,1 FROM sync_events WHERE event_id=$1`, categorySource, canonical)
	for rev := int64(2); rev <= 106; rev++ {
		e := r2EventID(9400 + int(rev))
		f.ingest(t, f.devA, f.credA, e, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"ar": "ذهب", "en": fmt.Sprintf("Gold %d", rev)}, rev))
		r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,last_error_code,last_error_message) VALUES($1,$2,'blocked',1,'CATALOG_REVISION_CONFLICT','tag identity collision')`, e, catalog.ProcessorTagProjectionV1)
	}
	// Current references from processed sources must transition alongside defaults.
	product := "a9000000-0000-4000-8000-000000009801"
	pe := r2EventID(9801)
	f.ingest(t, f.devA, f.credA, pe, catalog.EventProductSnapshotV1, productPayload(product, "R4-MIXED", "Product", sharedCatIslamic, nil, []string{sharedTagGold}, 1))
	r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, pe, catalog.ProcessorProductProjectionV1)
	r4Exec(t, f, 1, `INSERT INTO catalog_products(product_id,sku,name,top_category_id,width_cm,height_cm,is_active,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) SELECT $2,'R4-MIXED','Product',$3,70,100,true,1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, pe, product, sharedCatIslamic)
	r4Exec(t, f, 1, `INSERT INTO catalog_product_tags VALUES($1,$2)`, product, sharedTagGold)
	r4Exec(t, f, 1, `INSERT INTO catalog_product_translations(product_id,locale,name) VALUES($1,'ar','Product')`, product)
	r4Exec(t, f, 2, `INSERT INTO catalog_product_prices VALUES($1,'EGP',65000,40000),($1,'USD',1300,NULL)`, product)

	child := "a9000000-0000-4000-8000-000000009802"
	ce := r2EventID(9802)
	f.ingest(t, f.devA, f.credA, ce, catalog.EventCategorySnapshotV1, categoryPayload(child, "active", map[string]string{"ar": "فرع", "en": "Child"}, []string{sharedCatIslamic}, 1))
	r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, ce, catalog.ProcessorCategoryProjectionV1)
	r4Exec(t, f, 1, `INSERT INTO catalog_categories(category_id,status,name_ar,name_en,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) SELECT $2,'active','فرع','Child',1,event_id,device_id,payload_hash,received_at,store_id FROM sync_events WHERE event_id=$1`, ce, child)
	r4Exec(t, f, 1, `INSERT INTO catalog_category_edges VALUES($1,$2,0)`, sharedCatIslamic, child)
	genuine := r2EventID(9803)
	f.ingest(t, f.devA, f.credA, genuine, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Conflict"}, 1))
	r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,last_error_code,last_error_message) VALUES($1,$2,'blocked',1,'CATALOG_REVISION_CONFLICT','equal revision with conflicting state')`, genuine, catalog.ProcessorTagProjectionV1)
	legacy := r2EventID(9804)
	lp := "a9000000-0000-4000-8000-000000009804"
	f.ingest(t, f.devC, f.credC, legacy, catalog.EventProductSnapshotV1, productPayload(lp, "R4-LEGACY", "Legacy", sharedCatIslamic, nil, nil, 1))
	r4Exec(t, f, 1, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count,processed_at) VALUES($1,$2,'processed',1,now())`, legacy, catalog.ProcessorProductProjectionV1)
	r4Exec(t, f, 1, `INSERT INTO catalog_products(product_id,sku,name,top_category_id,width_cm,height_cm,is_active,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at) SELECT $2,'R4-LEGACY','Legacy',$3,70,100,true,1,event_id,device_id,payload_hash,received_at FROM sync_events WHERE event_id=$1`, legacy, lp, sharedCatIslamic)
	before := r4Payloads(t, f)
	d := NewDevices(f.pool, 5*time.Second)
	// Failure after earlier reset attempts rolls the entire batch back.
	r4Exec(t, f, 0, `CREATE FUNCTION r4_reset_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_id='e0000900-0000-4000-8000-000000009403'::uuid THEN RAISE EXCEPTION 'injected reset failure'; END IF; RETURN NEW; END $$`)
	r4Exec(t, f, 0, `CREATE TRIGGER r4_reset_fail BEFORE UPDATE ON sync_event_processing FOR EACH ROW EXECUTE FUNCTION r4_reset_fail()`)
	if _, err := d.RecoverDefaultCatalogBlocked(ctx); err == nil {
		t.Fatal("fault did not abort recovery")
	}
	var pending int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM sync_event_processing WHERE status='pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("partial reset=%d %v", pending, err)
	}
	r4Exec(t, f, 0, `DROP TRIGGER r4_reset_fail ON sync_event_processing`)
	counts := []int64{}
	for cycle := 0; cycle < 5; cycle++ {
		n, err := d.RecoverDefaultCatalogBlocked(ctx)
		if err != nil || n > 100 {
			t.Fatalf("batch=%d %v", n, err)
		}
		counts = append(counts, n)
		if n == 0 {
			break
		}
		if cycle == 0 && n != 100 {
			t.Fatalf("first batch=%d", n)
		}
		// Discard repository/pool handles after durable queueing, then resume.
		reopened, _ := openTestRepoOnURL(t, f.pool.Config().ConnString())
		f.pool = reopened
		d = NewDevices(reopened, 5*time.Second)
		r3Converge(t, f)
	}
	if counts[len(counts)-1] != 0 {
		t.Fatalf("no final zero: %v", counts)
	}
	var processed int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM sync_event_processing p JOIN sync_events e USING(event_id) WHERE p.processor=$1 AND p.status='processed' AND e.payload->>'tag_id'=$2 AND (e.payload->>'catalog_revision')::bigint BETWEEN 2 AND 106`, catalog.ProcessorTagProjectionV1, sharedTagGold).Scan(&processed); err != nil || processed != 105 {
		t.Fatalf("obsolete updates=%d %v", processed, err)
	}
	var revision int64
	if err := f.pool.QueryRow(ctx, `SELECT source_revision FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreA)).Scan(&revision); err != nil || revision != 106 {
		t.Fatalf("final revision=%d %v", revision, err)
	}
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, genuine, catalog.ProcessorTagProjectionV1).Scan(&status); err != nil || status != "blocked" {
		t.Fatal("genuine conflict changed", err)
	}
	var refs bool
	if err := f.pool.QueryRow(ctx, `SELECT p.top_category_id=$2 AND EXISTS(SELECT 1 FROM catalog_product_tags t WHERE t.product_id=p.product_id AND t.tag_id=$3) FROM catalog_products p WHERE p.product_id=$1`, product, canonical, scopedTagID(t, sharedTagGold, scopeStoreA)).Scan(&refs); err != nil || !refs {
		t.Fatal("product references not canonical", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_category_edges WHERE child_id=$1 AND parent_id=$2)`, child, canonical).Scan(&refs); err != nil || !refs {
		t.Fatal("edge not canonical", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT store_id IS NULL AND top_category_id=$2 FROM catalog_products WHERE product_id=$1`, lp, sharedCatIslamic).Scan(&refs); err != nil || !refs {
		t.Fatal("legacy references changed", err)
	}
	if before != r4Payloads(t, f) {
		t.Fatal("payload/hash changed")
	}
	t.Logf("mixed recovery counts=%v; 105 obsolete updates processed; canonical revision=106; genuine block and unbound raw reference preserved", counts)
}

func TestR4_RecoveryRacingProjection(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		f := openScopeFixture(t)
		ctx := context.Background()
		r4Raw(t, f, false)
		newer := r2EventID(9900)
		f.ingest(t, f.devA, f.credA, newer, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"ar": "جديد", "en": "New"}, 2))
		var wg sync.WaitGroup
		start := make(chan struct{})
		var recoveryErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, recoveryErr = NewDevices(f.pool, 5*time.Second).RecoverDefaultCatalogBlocked(ctx)
		}()
		go func() { defer wg.Done(); <-start; r3Project(t, f, newer, catalog.EventTagSnapshotV1) }()
		close(start)
		wg.Wait()
		if recoveryErr != nil {
			t.Logf("serialized recovery abort, retrying: %v", recoveryErr)
		}
		r3Converge(t, f)
		d := NewDevices(f.pool, 5*time.Second)
		n, err := d.RecoverDefaultCatalogBlocked(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			r3Converge(t, f)
		}
		n, err = d.RecoverDefaultCatalogBlocked(ctx)
		if err != nil || n != 0 {
			t.Fatalf("race final=%d %v", n, err)
		}
		var rev int64
		var name string
		if err = f.pool.QueryRow(ctx, `SELECT source_revision,name_en FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreA)).Scan(&rev, &name); err != nil || rev != 2 || name != "New" {
			t.Fatalf("race regression=%d %s %v", rev, name, err)
		}
		var retired bool
		if err = f.pool.QueryRow(ctx, `SELECT store_id IS NULL AND default_algorithm=2 FROM catalog_tags WHERE tag_id=$1`, sharedTagGold).Scan(&retired); err != nil || !retired {
			t.Fatalf("race retirement=%v %v", retired, err)
		}
	}
}
