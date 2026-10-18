package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/google/uuid"
)

func TestSharedProductTypeIndependentStores(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewCatalogAdmin(f.pool, 5*time.Second)
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
		project(actor.dev, actor.cred, catalog.EventCategorySnapshotV2, categoryPayload(cid, "active", map[string]string{"ar": "قسم"}, nil, 1))
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
