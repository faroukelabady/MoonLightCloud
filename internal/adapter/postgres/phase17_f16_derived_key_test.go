package postgres

// Phase 17-R3 F16: no event from one Store can occupy another Store's
// derived seed ProductType storage key. Derived keys are UUIDv5; Retail
// ProductType identities are v4, so v5 Type identities are refused at
// ingestion and blocked terminally if previously accepted. Installations
// that accepted an occupant before F16 keep it untouched and surface a
// specific diagnostic. Real PostgreSQL.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/google/uuid"
)

func f16StoreKey(store string) string {
	return canonicalDefaultProductTypeID(sharedProductTypeID, storeUUID(&store))
}

func f16Ingest(f *scopeFixture, devID, credID, eventID, eventType, payload string) (string, error) {
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, eventType, payload)
	res, err := f.syncSvc.Ingest(context.Background(), devID, credID, []byte(body))
	if err != nil {
		return "", err
	}
	return res.Events[0].Status, nil
}

func f16Count(t *testing.T, f *scopeFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func f16ProductPayload(t *testing.T, productID, categoryID, typeID string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &payload); err != nil {
		t.Fatal(err)
	}
	payload["product_id"], payload["top_category_id"], payload["product_type_id"] = productID, categoryID, typeID
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// projectSeedAndProduct proves a Store's genuine seed and a Product using it
// converge on that Store's own derived storage key.
func f16ProjectSeedAndProduct(t *testing.T, f *scopeFixture, dev, cred, store string) {
	t.Helper()
	project := func(eventType, payload string) {
		t.Helper()
		eid := uuid.NewString()
		f.ingest(t, dev, cred, eid, eventType, payload)
		if res := projectCatalogTerminal(t, f, eid, eventType); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("%s: %+v", eventType, res)
		}
	}
	project(catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
	cid, pid := uuid.NewString(), uuid.NewString()
	project(catalog.EventCategorySnapshotV1, categoryPayload(cid, "active", map[string]string{"ar": "قسم"}, nil, 1))
	project(catalog.EventProductSnapshotV2, f16ProductPayload(t, pid, cid, sharedProductTypeID))
	var relation string
	if err := f.pool.QueryRow(context.Background(), `SELECT product_type_id::text FROM catalog_products WHERE product_id=$1 AND store_id=$2`, pid, store).Scan(&relation); err != nil || relation != f16StoreKey(store) {
		t.Fatalf("product relation %q want %q: %v", relation, f16StoreKey(store), err)
	}
}

func TestF16DerivedKeyRefusedAtIngestion(t *testing.T) {
	f := openScopeFixture(t)
	keyA := f16StoreKey(scopeStoreA)

	// Store B, an unbound legacy device and a Product reference all fail
	// to introduce Store A's derived key; nothing durable is written.
	attempts := []struct {
		name, dev, cred, eventType, payload string
	}{
		{"store B type", f.devB, f.credB, catalog.EventProductTypeSnapshotV1, typePayload(keyA, "squat", 1)},
		{"legacy unbound type", f.devC, f.credC, catalog.EventProductTypeSnapshotV1, typePayload(keyA, "squat", 1)},
		{"store B product reference", f.devB, f.credB, catalog.EventProductSnapshotV2, f16ProductPayload(t, uuid.NewString(), uuid.NewString(), keyA)},
	}
	for _, attempt := range attempts {
		eid := uuid.NewString()
		status, err := f16Ingest(f, attempt.dev, attempt.cred, eid, attempt.eventType, attempt.payload)
		if err == nil && status == "accepted" {
			t.Fatalf("%s: derived storage identity accepted", attempt.name)
		}
		if n := f16Count(t, f, `SELECT count(*) FROM sync_events WHERE event_id=$1`, eid); n != 0 {
			t.Fatalf("%s: refused event persisted", attempt.name)
		}
	}
	if n := f16Count(t, f, `SELECT count(*) FROM catalog_product_types`); n != 0 {
		t.Fatalf("refused identities created %d type rows", n)
	}
	// Store A's genuine seed and Product still converge normally, and an
	// ordinary v4 custom Type from Store B is unaffected.
	f16ProjectSeedAndProduct(t, f, f.devA, f.credA, scopeStoreA)
	custom := uuid.NewString()
	eid := uuid.NewString()
	f.ingest(t, f.devB, f.credB, eid, catalog.EventProductTypeSnapshotV1, typePayload(custom, "souvenir", 1))
	if res := projectCatalogTerminal(t, f, eid, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("v4 custom type: %+v", res)
	}
}

func TestF16PreviouslyAcceptedDerivedKeyBlocksTerminally(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	keyA := f16StoreKey(scopeStoreA)
	eid := uuid.NewString()
	payload := typePayload(keyA, "squat", 1)
	hash := sha256.Sum256([]byte(payload))
	if _, err := f.pool.Exec(ctx, `INSERT INTO sync_events (event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id)
		VALUES ($1,$2,$3,now(),$4,$5,$6)`, eid, f.devB, catalog.EventProductTypeSnapshotV1, payload, hash[:], scopeStoreB); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		res := projectCatalogTerminal(t, f, eid, catalog.EventProductTypeSnapshotV1)
		if res.Outcome != catalog.OutcomeBlocked {
			t.Fatalf("pre-accepted derived identity must block terminally: %+v", res)
		}
	}
	var code string
	if err := f.pool.QueryRow(ctx, `SELECT last_error_code FROM sync_event_processing WHERE event_id=$1`, eid).Scan(&code); err != nil || code != ErrValidation {
		t.Fatalf("blocked code %q: %v", code, err)
	}
	if n := f16Count(t, f, `SELECT count(*) FROM catalog_product_types`); n != 0 {
		t.Fatalf("blocked event wrote %d rows", n)
	}
	f16ProjectSeedAndProduct(t, f, f.devA, f.credA, scopeStoreA)
}

// An installation that accepted an occupant before F16 keeps it unchanged;
// Store A's seed reports the specific diagnostic and recovery does not
// re-arm, rewrite, adopt or delete anything.
func TestF16LegacyForeignOccupantPreservedWithDiagnostic(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	keyA := f16StoreKey(scopeStoreA)
	occupantEvent := uuid.NewString()
	occupantPayload := typePayload(keyA, "squat", 1)
	hash := sha256.Sum256([]byte(occupantPayload))
	if _, err := f.pool.Exec(ctx, `INSERT INTO sync_events (event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id)
		VALUES ($1,$2,$3,now(),$4,$5,$6)`, occupantEvent, f.devB, catalog.EventProductTypeSnapshotV1, occupantPayload, hash[:], scopeStoreB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO catalog_product_types(type_id,code,name_ar,name_en,is_active,position,type_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
		VALUES($1,'squat','برديات','Squat',true,0,1,$2,$3,'\x00',now(),$4)`, keyA, occupantEvent, f.devB, scopeStoreB); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := f.pool.QueryRow(ctx, `SELECT row_to_json(t)::text FROM catalog_product_types t WHERE type_id=$1`, keyA).Scan(&before); err != nil {
		t.Fatal(err)
	}

	seed := uuid.NewString()
	f.ingest(t, f.devA, f.credA, seed, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
	if res := projectCatalogTerminal(t, f, seed, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeBlocked {
		t.Fatalf("seed over foreign occupant: %+v", res)
	}
	var message string
	if err := f.pool.QueryRow(ctx, `SELECT last_error_message FROM sync_event_processing WHERE event_id=$1`, seed).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message != "product type seed storage key occupied by another store" {
		t.Fatalf("diagnostic %q", message)
	}
	rearmed, err := d.RecoverDefaultCatalogBlocked(ctx)
	if err != nil || rearmed != 0 {
		t.Fatalf("recovery re-armed %d: %v", rearmed, err)
	}
	var after string
	if err := f.pool.QueryRow(ctx, `SELECT row_to_json(t)::text FROM catalog_product_types t WHERE type_id=$1`, keyA).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("occupant changed:\n%s\n%s", before, after)
	}
	if n := f16Count(t, f, `SELECT count(*) FROM catalog_product_types WHERE store_id=$1`, scopeStoreA); n != 0 {
		t.Fatalf("store A adopted the occupant: %d rows", n)
	}
}

func TestF16ConcurrentSeedAndSquatAttempts(t *testing.T) {
	for run := 0; run < 20; run++ {
		t.Run(fmt.Sprint(run), func(t *testing.T) {
			f := openScopeFixture(t)
			keyA := f16StoreKey(scopeStoreA)
			seed := uuid.NewString()
			var wg sync.WaitGroup
			var squatAccepted bool
			wg.Add(2)
			go func() {
				defer wg.Done()
				status, err := f16Ingest(f, f.devB, f.credB, uuid.NewString(), catalog.EventProductTypeSnapshotV1, typePayload(keyA, "squat", 1))
				squatAccepted = err == nil && status == "accepted"
			}()
			go func() {
				defer wg.Done()
				status, err := f16Ingest(f, f.devA, f.credA, seed, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
				if err != nil || status != "accepted" {
					t.Errorf("seed ingest: %q %v", status, err)
				}
			}()
			wg.Wait()
			if squatAccepted {
				t.Fatal("squat accepted")
			}
			if res := projectCatalogTerminal(t, f, seed, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
				t.Fatalf("seed: %+v", res)
			}
			if n := f16Count(t, f, `SELECT count(*) FROM catalog_product_types WHERE type_id=$1 AND store_id=$2`, keyA, scopeStoreA); n != 1 {
				t.Fatalf("store A seed rows %d", n)
			}
			if n := f16Count(t, f, `SELECT count(*) FROM catalog_product_types WHERE store_id IS DISTINCT FROM $1`, scopeStoreA); n != 0 {
				t.Fatalf("squat rows %d", n)
			}
		})
	}
}
