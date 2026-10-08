package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/google/uuid"
)

func TestSharedProductTypeLegacyCustomAliasPreserved(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	store := storeUUID(func() *string { s := scopeStoreA; return &s }())
	alias := canonicalDefaultProductTypeID(sharedProductTypeID, store)
	eventID := uuid.NewString()
	insertPreF16Event(t, f, f.devA, eventID, catalog.EventProductTypeSnapshotV1, typePayload(alias, "custom", 1), scopeStoreA)
	// This occupied alias predates the reservation guard. Its source is a
	// genuine custom identity, not the Retail seed; never adopt or relabel it.
	if _, err := f.pool.Exec(ctx, `INSERT INTO catalog_product_types(type_id,code,name_ar,name_en,is_active,position,type_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
VALUES($1,'custom','مخصص','Custom',true,0,1,$2,$3,'\x00',now(),$4)`, alias, eventID, f.devA, scopeStoreA); err != nil {
		t.Fatal(err)
	}
	if _, err := projectedProductTypeID(ctx, sqlcgen.New(f.pool), sharedProductTypeID, store); !errors.Is(err, ErrProductTypeIdentityCollision) {
		t.Fatalf("occupied custom alias accepted as seed: %v", err)
	}
	list, err := d.AdminProductTypeList(ctx, scopeStoreA)
	if err != nil || len(list) != 1 || list[0].TypeID != alias {
		t.Fatalf("custom identity masked by Type list: %+v %v", list, err)
	}
	detail, err := d.AdminProductType(ctx, scopeStoreA, alias)
	if err != nil || detail.TypeID != alias || detail.Code != "custom" {
		t.Fatalf("custom identity masked by Type detail: %+v %v", detail, err)
	}
	if _, err := d.AdminProductType(ctx, scopeStoreA, sharedProductTypeID); kindOf(err) != apperr.Conflict {
		t.Fatalf("ambiguous seed lookup must conflict: %v", err)
	}
	cid, pid := uuid.NewString(), uuid.NewString()
	ce := uuid.NewString()
	f.ingest(t, f.devA, f.credA, ce, catalog.EventCategorySnapshotV1, categoryPayload(cid, "active", map[string]string{"ar": "قسم"}, nil, 1))
	if result := projectCatalogTerminal(t, f, ce, catalog.EventCategorySnapshotV1); result.Outcome != catalog.OutcomeProcessed {
		t.Fatal(result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &payload); err != nil {
		t.Fatal(err)
	}
	payload["product_id"], payload["top_category_id"], payload["product_type_id"] = pid, cid, alias
	raw, _ := json.Marshal(payload)
	pe := uuid.NewString()
	// Its Product relationship was also projected before F16 refused
	// derived identities at ingestion; reproduce that stored state.
	insertPreF16Event(t, f, f.devA, pe, catalog.EventProductSnapshotV2, string(raw), scopeStoreA)
	if _, err := f.pool.Exec(ctx, `INSERT INTO catalog_products(product_id,name,top_category_id,is_active,product_type_id,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
VALUES($1,'منتج',$2,true,$3,1,$4,$5,'\x00',now(),$6)`, pid, cid, alias, pe, f.devA, scopeStoreA); err != nil {
		t.Fatal(err)
	}
	product, err := d.AdminProductDetail(ctx, scopeStoreA, pid)
	if err != nil || product.ProductTypeID != alias {
		t.Fatalf("custom identity masked by Product detail: %+v %v", product, err)
	}
	products, err := d.AdminProductList(ctx, scopeStoreA, "", "", 100)
	if err != nil || len(products) != 1 || products[0].ProductTypeID != alias {
		t.Fatalf("custom identity masked by Product list: %+v %v", products, err)
	}
	seedEvent := uuid.NewString()
	f.ingest(t, f.devA, f.credA, seedEvent, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 99))
	if result := projectCatalogTerminal(t, f, seedEvent, catalog.EventProductTypeSnapshotV1); result.Outcome != catalog.OutcomeBlocked || result.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("seed overwrote occupied custom alias: %+v", result)
	}
	var code, source, relation string
	var revision int64
	if err := f.pool.QueryRow(ctx, `SELECT code,type_revision,source_event_id::text FROM catalog_product_types WHERE type_id=$1`, alias).Scan(&code, &revision, &source); err != nil || code != "custom" || revision != 1 || source != eventID {
		t.Fatalf("custom row changed: %q %d %q %v", code, revision, source, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT product_type_id::text FROM catalog_products WHERE product_id=$1`, pid).Scan(&relation); err != nil || relation != alias {
		t.Fatalf("custom Product relation changed: %q %v", relation, err)
	}
	// Reprocessing that pre-F16 Product event refuses the derived identity
	// terminally and leaves the preserved relationship untouched.
	if result := projectCatalogTerminal(t, f, pe, catalog.EventProductSnapshotV2); result.Outcome != catalog.OutcomeBlocked || result.ErrorCode != ErrValidation {
		t.Fatalf("pre-F16 derived Product reference must block: %+v", result)
	}
	if err := f.pool.QueryRow(ctx, `SELECT product_type_id::text FROM catalog_products WHERE product_id=$1`, pid).Scan(&relation); err != nil || relation != alias {
		t.Fatalf("custom Product relation changed by reprocessing: %q %v", relation, err)
	}
}

// insertPreF16Event persists an event exactly as ingestion stored it before
// Phase 17-R3 F16 refused derived storage identities at the gate; store ""
// models an unbound legacy device.
func insertPreF16Event(t *testing.T, f *scopeFixture, deviceID, eventID, eventType, payload, store string) {
	t.Helper()
	hash := sha256.Sum256([]byte(payload))
	var scope any
	if store != "" {
		scope = store
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO sync_events (event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id)
		VALUES ($1,$2,$3,now(),$4,$5,$6)`, eventID, deviceID, eventType, payload, hash[:], scope); err != nil {
		t.Fatal(err)
	}
}

func TestSharedProductTypeReservedAliasNewEventBlocked(t *testing.T) {
	f := openScopeFixture(t)
	store := storeUUID(func() *string { s := scopeStoreA; return &s }())
	alias := canonicalDefaultProductTypeID(sharedProductTypeID, store)
	// F16: a new derived identity is refused at ingestion with no durable row.
	refused := uuid.NewString()
	if status, err := f16Ingest(f, f.devA, f.credA, refused, catalog.EventProductTypeSnapshotV1, typePayload(alias, "custom", 1)); err == nil && status == "accepted" {
		t.Fatal("derived identity accepted at ingestion")
	}
	// An event accepted before F16 still cannot occupy the reserved key.
	eventID := uuid.NewString()
	insertPreF16Event(t, f, f.devA, eventID, catalog.EventProductTypeSnapshotV1, typePayload(alias, "custom", 1), scopeStoreA)
	if result := projectCatalogTerminal(t, f, eventID, catalog.EventProductTypeSnapshotV1); result.Outcome != catalog.OutcomeBlocked || result.ErrorCode != ErrValidation {
		t.Fatalf("pre-F16 custom event occupied reserved key: %+v", result)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM catalog_product_types`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reserved-key rejection left Type state: %d %v", count, err)
	}
}

func TestSharedProductTypeUnscopedAliasCannotOverwriteSeed(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	store := storeUUID(func() *string { s := scopeStoreA; return &s }())
	alias := canonicalDefaultProductTypeID(sharedProductTypeID, store)
	seedEvent := uuid.NewString()
	f.ingest(t, f.devA, f.credA, seedEvent, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
	if result := projectCatalogTerminal(t, f, seedEvent, catalog.EventProductTypeSnapshotV1); result.Outcome != catalog.OutcomeProcessed {
		t.Fatal(result)
	}
	legacyEvent := uuid.NewString()
	// A real authenticated, unbound legacy device has no ingress Store.
	// Its chosen raw UUID must not become writable authority over a proven
	// Store's internal seed projection, even at a higher revision.
	// F16 refuses this at ingestion; model an event accepted before F16.
	insertPreF16Event(t, f, f.devC, legacyEvent, catalog.EventProductTypeSnapshotV1, typePayload(alias, "custom", 99), "")
	var nullScope bool
	if err := f.pool.QueryRow(ctx, `SELECT store_id IS NULL FROM sync_events WHERE event_id=$1`, legacyEvent).Scan(&nullScope); err != nil || !nullScope {
		t.Fatal("legacy fixture unexpectedly scoped", nullScope, err)
	}
	result := projectCatalogTerminal(t, f, legacyEvent, catalog.EventProductTypeSnapshotV1)
	var code, source, owner string
	var revision int64
	if err := f.pool.QueryRow(ctx, `SELECT code,type_revision,source_event_id::text,store_id::text FROM catalog_product_types WHERE type_id=$1`, alias).Scan(&code, &revision, &source, &owner); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != catalog.OutcomeBlocked || result.ErrorCode != ErrValidation || code != "papyrus" || revision != 1 || source != seedEvent || owner != scopeStoreA {
		t.Fatalf("unscoped raw alias changed seeded projection: result=%+v code=%q revision=%d source=%q owner=%q", result, code, revision, source, owner)
	}
	cid, pid, categoryEvent, productEvent := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	f.ingest(t, f.devC, f.credC, categoryEvent, catalog.EventCategorySnapshotV1, categoryPayload(cid, "active", map[string]string{"ar": "قسم"}, nil, 1))
	if result := projectCatalogTerminal(t, f, categoryEvent, catalog.EventCategorySnapshotV1); result.Outcome != catalog.OutcomeProcessed {
		t.Fatal(result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &payload); err != nil {
		t.Fatal(err)
	}
	payload["product_id"], payload["top_category_id"], payload["product_type_id"] = pid, cid, alias
	raw, _ := json.Marshal(payload)
	insertPreF16Event(t, f, f.devC, productEvent, catalog.EventProductSnapshotV2, string(raw), "")
	if result := projectCatalogTerminal(t, f, productEvent, catalog.EventProductSnapshotV2); result.Outcome != catalog.OutcomeBlocked || result.ErrorCode != ErrValidation {
		t.Fatalf("raw internal alias accepted as a Product relationship: %+v", result)
	}
	var productCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_products WHERE product_id=$1`, pid).Scan(&productCount); err != nil || productCount != 0 {
		t.Fatalf("rejected raw-alias relationship left Product state: %d %v", productCount, err)
	}
}

func TestSharedProductTypeUnscopedSeedCannotOverwriteOwnedRaw(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	eventID := uuid.NewString()
	f.ingest(t, f.devA, f.credA, eventID, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 1))
	// An existing installation already projected the raw seed under Store A.
	// Preserve this valid old storage shape and its genuine scoped source.
	if _, err := f.pool.Exec(ctx, `INSERT INTO catalog_product_types(type_id,code,name_ar,name_en,is_active,position,type_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
VALUES($1,'papyrus','برديات','Papyrus',true,0,1,$2,$3,'\x00',now(),$4)`, sharedProductTypeID, eventID, f.devA, scopeStoreA); err != nil {
		t.Fatal(err)
	}
	legacyEvent := uuid.NewString()
	f.ingest(t, f.devC, f.credC, legacyEvent, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "custom", 99))
	result := projectCatalogTerminal(t, f, legacyEvent, catalog.EventProductTypeSnapshotV1)
	var code, source, owner string
	var revision int64
	if err := f.pool.QueryRow(ctx, `SELECT code,type_revision,source_event_id::text,store_id::text FROM catalog_product_types WHERE type_id=$1`, sharedProductTypeID).Scan(&code, &revision, &source, &owner); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != catalog.OutcomeBlocked || result.ErrorCode != ErrCatalogRevisionConflict || code != "papyrus" || revision != 1 || source != eventID || owner != scopeStoreA {
		t.Fatalf("unscoped seed changed owned raw Type: result=%+v code=%q revision=%d source=%q owner=%q", result, code, revision, source, owner)
	}
	scopedEvent := uuid.NewString()
	f.ingest(t, f.devA, f.credA, scopedEvent, catalog.EventProductTypeSnapshotV1, typePayload(sharedProductTypeID, "papyrus", 2))
	if result := projectCatalogTerminal(t, f, scopedEvent, catalog.EventProductTypeSnapshotV1); result.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("owner can no longer advance preserved raw Type: %+v", result)
	}
	if err := f.pool.QueryRow(ctx, `SELECT code,type_revision,source_event_id::text,store_id::text FROM catalog_product_types WHERE type_id=$1`, sharedProductTypeID).Scan(&code, &revision, &source, &owner); err != nil || code != "papyrus" || revision != 2 || source != scopedEvent || owner != scopeStoreA {
		t.Fatalf("owner update did not preserve raw Type identity: %q %d %q %q %v", code, revision, source, owner, err)
	}
}
