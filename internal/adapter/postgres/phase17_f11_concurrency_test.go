package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/google/uuid"
)

// Existing Store A retains its pre-fix raw storage identity while a new
// Store B uses a scoped key. Both still emit the same frozen Retail ID;
// concurrent revision streams must remain independent.
func TestSharedProductTypeOwnedRawAndScopedConcurrentRevisions(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	payload := func(name string, revision int64) string {
		t.Helper()
		var state map[string]any
		if err := json.Unmarshal([]byte(typePayload(sharedProductTypeID, "papyrus", revision)), &state); err != nil {
			t.Fatal(err)
		}
		state["name_en"] = name
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	// Simulate the actual pre-fix owned projection with an immutable source
	// event accepted under A, not a fabricated ownership-only row.
	seedEvent := uuid.NewString()
	seedPayload := payload("A-1", 1)
	f.ingest(t, f.devA, f.credA, seedEvent, catalog.EventProductTypeSnapshotV1, seedPayload)
	decoded, err := catalog.DecodeProductTypeSnapshot([]byte(seedPayload))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := catalog.ValidateProductTypeSnapshot(decoded)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := catalog.FingerprintProductType(valid)
	if _, err := f.pool.Exec(ctx, `INSERT INTO catalog_product_types
		(type_id,code,name_ar,name_en,is_active,position,type_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
		VALUES($1,'papyrus','برديات','A-1',true,0,1,$2,$3,$4,now(),$5)`,
		sharedProductTypeID, seedEvent, f.devA, fingerprint[:], scopeStoreA); err != nil {
		t.Fatal(err)
	}
	bEvent := uuid.NewString()
	f.ingest(t, f.devB, f.credB, bEvent, catalog.EventProductTypeSnapshotV1, payload("B-1", 1))
	if result := projectCatalogTerminal(t, f, bEvent, catalog.EventProductTypeSnapshotV1); result.Outcome != catalog.OutcomeProcessed {
		t.Fatal(result)
	}

	// Preserve accepted source payloads/hashes across all race attempts.
	readSource := func(id string) string {
		t.Helper()
		var state string
		if err := f.pool.QueryRow(ctx, `SELECT payload::text || '|' || encode(payload_hash,'hex') FROM sync_events WHERE event_id=$1`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	sources := map[string]string{seedEvent: readSource(seedEvent), bEvent: readSource(bEvent)}
	lastA, lastB := seedEvent, bEvent
	for revision := int64(2); revision <= 21; revision++ {
		aEvent, bEvent := uuid.NewString(), uuid.NewString()
		f.ingest(t, f.devA, f.credA, aEvent, catalog.EventProductTypeSnapshotV1, payload(fmt.Sprintf("A-%d", revision), revision))
		f.ingest(t, f.devB, f.credB, bEvent, catalog.EventProductTypeSnapshotV1, payload(fmt.Sprintf("B-%d", revision), revision))
		sources[aEvent], sources[bEvent] = readSource(aEvent), readSource(bEvent)
		start := make(chan struct{})
		results := make(chan catalogWorkerResult, 2)
		for _, id := range []string{aEvent, bEvent} {
			go func(eventID string) {
				<-start
				// Reuse the production-attempt test seam: only the explicit
				// transient contract can retry, bounded by catalogRetryBudget.
				results <- runCatalogWorker(d, eventID, catalog.EventProductTypeSnapshotV1, catalogRetryBudget)
			}(id)
		}
		close(start)
		for range 2 {
			result := <-results
			reportCatalogWorkerError(t, result.Err)
			if result.Result.Outcome != catalog.OutcomeProcessed {
				t.Fatalf("revision %d: %+v", revision, result)
			}
		}
		lastA, lastB = aEvent, bEvent
	}

	canonicalB := canonicalDefaultProductTypeID(sharedProductTypeID, storeUUID(func() *string { s := scopeStoreB; return &s }()))
	if canonicalB == sharedProductTypeID {
		t.Fatal("new Store storage identity must differ from preserved raw identity")
	}
	for _, expected := range []struct{ store, id, name, source string }{
		{scopeStoreA, sharedProductTypeID, "A-21", lastA},
		{scopeStoreB, canonicalB, "B-21", lastB},
	} {
		var id, name, source string
		var revision int64
		if err := f.pool.QueryRow(ctx, `SELECT type_id::text,name_en,type_revision,source_event_id::text
			FROM catalog_product_types WHERE store_id=$1`, expected.store).Scan(&id, &name, &revision, &source); err != nil {
			t.Fatal(err)
		}
		if id != expected.id || name != expected.name || revision != 21 || source != expected.source {
			t.Fatalf("Store %s: id=%s name=%s revision=%d source=%s", expected.store, id, name, revision, source)
		}
		view, err := d.AdminProductType(ctx, expected.store, sharedProductTypeID)
		if err != nil || view.TypeID != sharedProductTypeID || view.NameEN != expected.name || view.TypeRevision != 21 {
			t.Fatalf("Store source identity/read: %+v %v", view, err)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_product_types`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("storage rows=%d: %v", count, err)
	}
	for id, before := range sources {
		if after := readSource(id); after != before {
			t.Fatalf("accepted source event %s changed across projection races", id)
		}
	}
}
