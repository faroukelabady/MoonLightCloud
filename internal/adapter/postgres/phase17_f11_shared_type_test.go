package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/google/uuid"
)

func TestSharedProductTypeIndependentStores(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	project := func(dev, cred, typ, payload string) string {
		t.Helper()
		eid := uuid.NewString()
		f.ingest(t, dev, cred, eid, typ, payload)
		if res := projectCatalogTerminal(t, f, eid, typ); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("%s: %+v", typ, res)
		}
		return eid
	}
	for _, actor := range []struct{ dev, cred, store, name string }{{f.devA, f.credA, scopeStoreA, "A"}, {f.devB, f.credB, scopeStoreB, "B"}} {
		project(actor.dev, actor.cred, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
		cid, pid := uuid.NewString(), uuid.NewString()
		project(actor.dev, actor.cred, catalog.EventCategorySnapshotV1, categoryPayload(cid, "active", map[string]string{"ar": "قسم"}, nil, 1))
		var payload map[string]any
		if err := json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &payload); err != nil {
			t.Fatal(err)
		}
		payload["product_id"] = pid
		payload["top_category_id"] = cid
		payload["product_type_id"] = sharedProductTypeID
		raw, _ := json.Marshal(payload)
		project(actor.dev, actor.cred, catalog.EventProductSnapshotV2, string(raw))
		scope := storeUUID(&actor.store)
		projected := canonicalDefaultProductTypeID(sharedProductTypeID, scope)
		var relation string
		if err := f.pool.QueryRow(ctx, `SELECT product_type_id::text FROM catalog_products WHERE product_id=$1 AND store_id=$2`, pid, actor.store).Scan(&relation); err != nil || relation != projected {
			t.Fatal(relation, projected, err)
		}
		row, err := d.AdminProductType(ctx, actor.store, sharedProductTypeID)
		if err != nil || row.TypeID != sharedProductTypeID {
			t.Fatal(row, err)
		}
		list, err := d.AdminProductTypeList(ctx, actor.store)
		if err != nil || len(list) != 1 || list[0].TypeID != sharedProductTypeID {
			t.Fatal(list, err)
		}
		product, err := d.AdminProductDetail(ctx, actor.store, pid)
		if err != nil || product.ProductTypeID != sharedProductTypeID {
			t.Fatal(product, err)
		}
		owner, rev, found, err := d.CatalogAdminOwnership(ctx, catalogadmin.TypeProductTypeDetailsUpdateV1, sharedProductTypeID, actor.store)
		if err != nil || !found || owner != actor.store || rev != 1 {
			t.Fatal(owner, rev, found, err)
		}
		rawType := typePayload(sharedProductTypeID, "papyrus", 2)
		var typeState map[string]any
		_ = json.Unmarshal([]byte(rawType), &typeState)
		typeState["name_en"] = actor.name
		raw, _ = json.Marshal(typeState)
		project(actor.dev, actor.cred, catalog.EventProductTypeSnapshotV1, string(raw))
		// Equal replay cannot compare another Store's revision or labels.
		project(actor.dev, actor.cred, catalog.EventProductTypeSnapshotV1, string(raw))
		row, err = d.AdminProductType(ctx, actor.store, sharedProductTypeID)
		if err != nil || row.NameEN != actor.name {
			t.Fatal(row, err)
		}
		// Commands carry the source ID and only converge after that Store's
		// normal projection advances; an internal storage alias is not intent.
		svc := catalogadmin.NewService(d, &stubAdminDevices{storeID: actor.store, active: map[string]bool{actor.dev: true}})
		commandPayload, _ := json.Marshal(map[string]any{"product_type_id": sharedProductTypeID, "name_en": actor.name + " updated"})
		view, err := svc.Create(ctx, "op", actor.store, catalogadmin.TypeProductTypeDetailsUpdateV1, sharedProductTypeID, 2, commandPayload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ReportCapabilities(ctx, actor.dev, []string{catalogadmin.CapabilityV1}); err != nil {
			t.Fatal(err)
		}
		wire, err := svc.Poll(ctx, actor.dev, 10)
		if err != nil || len(wire) != 1 || wire[0].EntityID != sharedProductTypeID {
			t.Fatal(wire, err)
		}
		if err := svc.ReportOutcome(ctx, actor.dev, wire[0].TargetID, catalogadmin.TargetApplied, catalogadmin.CodeApplied, sharedProductTypeID, 2, 3); err != nil {
			t.Fatal(err)
		}
		got, err := svc.Get(ctx, actor.store, view.ID)
		if err != nil || got.Converged {
			t.Fatal(got, err)
		}
		typeState["name_en"] = actor.name + " updated"
		typeState["type_revision"] = 3
		raw, _ = json.Marshal(typeState)
		project(actor.dev, actor.cred, catalog.EventProductTypeSnapshotV1, string(raw))
		got, err = svc.Get(ctx, actor.store, view.ID)
		if err != nil || !got.Converged {
			t.Fatal(got, err)
		}

	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_product_types`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	// A canonical storage key never authorizes another Store's Admin command.
	foreign := canonicalDefaultProductTypeID(sharedProductTypeID, storeUUID(func() *string { s := scopeStoreA; return &s }()))
	_, _, _, err := d.CatalogAdminOwnership(ctx, catalogadmin.TypeProductTypeDetailsUpdateV1, foreign, scopeStoreB)
	if err == nil {
		t.Fatal("internal alias accepted as raw command identity")
	}
	if _, err = d.AdminProductType(ctx, scopeStoreB, foreign); err == nil {
		t.Fatal("foreign type exposed")
	}
	svc := catalogadmin.NewService(d, &stubAdminDevices{storeID: scopeStoreB, active: map[string]bool{f.devB: true}})
	payload, _ := json.Marshal(map[string]any{"product_type_id": foreign, "name_en": "unauthorized"})
	if _, err := svc.Create(ctx, "op", scopeStoreB, catalogadmin.TypeProductTypeDetailsUpdateV1, foreign, 3, payload); err == nil {
		t.Fatal("foreign command accepted")
	}
}

func TestSharedProductTypePreservesOwnedRawAndRecoversCollision(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	eid := uuid.NewString()
	f.ingest(t, f.devA, f.credA, eid, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
	// Seed the exact pre-fix owned projection using its authoritative source.
	if _, err := f.pool.Exec(ctx, `INSERT INTO catalog_product_types(type_id,code,name_ar,name_en,is_active,position,type_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
 VALUES($1,'papyrus','برديات','Old A',true,0,1,$2,$3,'\x00',now(),$4)`, sharedProductTypeID, eid, f.devA, scopeStoreA); err != nil {
		t.Fatal(err)
	}
	// An existing Store's Product references remain the original source ID.
	acid, apid, apeid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ace := uuid.NewString()
	f.ingest(t, f.devA, f.credA, ace, catalog.EventCategorySnapshotV1, categoryPayload(acid, "active", map[string]string{"ar": "قسم"}, nil, 1))
	if res := projectCatalogTerminal(t, f, ace, catalog.EventCategorySnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	var oldProduct map[string]any
	_ = json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &oldProduct)
	oldProduct["product_id"] = apid
	oldProduct["top_category_id"] = acid
	oldProduct["product_type_id"] = sharedProductTypeID
	apraw, _ := json.Marshal(oldProduct)
	f.ingest(t, f.devA, f.credA, apeid, catalog.EventProductSnapshotV2, string(apraw))
	if res := projectCatalogTerminal(t, f, apeid, catalog.EventProductSnapshotV2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	be := uuid.NewString()
	f.ingest(t, f.devB, f.credB, be, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
	if _, err := d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, mustProductTypeUUID(be), time.Now(), ErrStoreScopeConflict, "product type owned by another store"); err != nil {
		t.Fatal(err)
	}
	// A Product blocked by the same dependency collision must recover too.
	cid, pid, peid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ce := uuid.NewString()
	f.ingest(t, f.devB, f.credB, ce, catalog.EventCategorySnapshotV1, categoryPayload(cid, "active", map[string]string{"ar": "قسم"}, nil, 1))
	if res := projectCatalogTerminal(t, f, ce, catalog.EventCategorySnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	var productPayload map[string]any
	if err := json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &productPayload); err != nil {
		t.Fatal(err)
	}
	productPayload["product_id"] = pid
	productPayload["top_category_id"] = cid
	productPayload["product_type_id"] = sharedProductTypeID
	raw, _ := json.Marshal(productPayload)
	f.ingest(t, f.devB, f.credB, peid, catalog.EventProductSnapshotV2, string(raw))
	if _, err := d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, mustProductTypeUUID(peid), time.Now(), ErrStoreScopeConflict, "product type owned by another store"); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := f.pool.QueryRow(ctx, `SELECT payload_hash FROM sync_events WHERE event_id=$1`, peid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if n, err := d.RecoverDefaultCatalogBlocked(ctx); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if res := projectCatalogTerminal(t, f, be, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	if res := projectCatalogTerminal(t, f, peid, catalog.EventProductSnapshotV2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	var after []byte
	if err := f.pool.QueryRow(ctx, `SELECT payload_hash FROM sync_events WHERE event_id=$1`, peid).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("payload changed", err)
	}
	if n, err := d.RecoverDefaultCatalogBlocked(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	projected, err := projectedProductTypeID(ctx, sqlcgen.New(f.pool), sharedProductTypeID, storeUUID(func() *string { s := scopeStoreA; return &s }()))
	if err != nil || projected != sharedProductTypeID {
		t.Fatal(projected, err)
	}
	row, err := d.AdminProductType(ctx, scopeStoreA, sharedProductTypeID)
	if err != nil || row.NameEN != "Old A" {
		t.Fatal(row, err)
	}
	var unchangedRelation string
	if err := f.pool.QueryRow(ctx, `SELECT product_type_id::text FROM catalog_products WHERE product_id=$1 AND store_id=$2`, apid, scopeStoreA).Scan(&unchangedRelation); err != nil || unchangedRelation != sharedProductTypeID {
		t.Fatal("old relation changed", unchangedRelation, err)
	}
	// Custom identities still cannot be shared across Stores.
	custom := uuid.NewString()
	a := uuid.NewString()
	f.ingest(t, f.devA, f.credA, a, catalog.EventProductTypeSnapshotV1, typePayload(custom, "book", 1))
	if res := projectCatalogTerminal(t, f, a, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	b := uuid.NewString()
	f.ingest(t, f.devB, f.credB, b, catalog.EventProductTypeSnapshotV1, typePayload(custom, "book", 1))
	if res := projectCatalogTerminal(t, f, b, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrStoreScopeConflict {
		t.Fatal(res)
	}
	if n, err := d.RecoverDefaultCatalogBlocked(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestSharedProductTypeUnownedLegacyRemainsUnowned(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	legacy := uuid.NewString()
	f.ingest(t, f.devC, f.credC, legacy, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
	if res := projectCatalogTerminal(t, f, legacy, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatal(res)
	}
	var before []byte
	if err := f.pool.QueryRow(ctx, `SELECT source_payload_hash FROM catalog_product_types WHERE type_id=$1 AND store_id IS NULL`, sharedProductTypeID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []struct{ dev, cred, store string }{{f.devA, f.credA, scopeStoreA}, {f.devB, f.credB, scopeStoreB}} {
		eid := uuid.NewString()
		f.ingest(t, actor.dev, actor.cred, eid, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 2))
		if res := projectCatalogTerminal(t, f, eid, catalog.EventProductTypeSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
			t.Fatal(res)
		}
		row, err := d.AdminProductType(ctx, actor.store, sharedProductTypeID)
		if err != nil || row.TypeRevision != 2 {
			t.Fatal(row, err)
		}
	}
	var after []byte
	if err := f.pool.QueryRow(ctx, `SELECT source_payload_hash FROM catalog_product_types WHERE type_id=$1 AND store_id IS NULL AND type_revision=1`, sharedProductTypeID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy rewritten", err)
	}
	if n, err := d.RecoverDefaultCatalogBlocked(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
