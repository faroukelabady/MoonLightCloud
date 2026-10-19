package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/google/uuid"
)

func policyPayload(productID string, revision int64, sellOffline, sellOnline bool, allocation *int) string {
	m := map[string]any{
		"product_id": productID, "sales_policy_revision": revision,
		"sell_offline": sellOffline, "sell_online": sellOnline,
	}
	if allocation != nil {
		m["online_allocation_limit"] = *allocation
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func policyInt(v int) *int { return &v }

// TestCatalogPolicyProjects proves the offline-only default projects with
// revision metadata once the core product exists.
func TestCatalogPolicyProjects(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 90, "pol1")
	productID := "e0002000-0000-4000-8000-000000000000"
	ingestCatalog(t, env, "d0003000-0000-4000-8000-000000000000", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-POL", "توت", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0003000-0000-4000-8000-000000000000")
	event := "d0003001-0000-4000-8000-000000000001"
	ingestCatalog(t, env, event, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 1, true, false, nil))
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("policy: %+v", res)
	}
	svc := catalog.NewService(catalogStore(env))
	policy, err := svc.GetProductSalesPolicy(context.Background(), productID)
	if err != nil {
		t.Fatalf("read service: %v", err)
	}
	if !policy.SellOffline || policy.SellOnline || policy.OnlineAllocationLimit != nil || policy.Revision != 1 {
		t.Fatalf("policy: %+v", policy)
	}
	if !catalog.IsStoreEligible(true, policy) || catalog.IsOnlineEligible(true, policy) {
		t.Fatalf("eligibility: %+v", policy)
	}
	if catalog.IsStoreEligible(false, policy) {
		t.Fatal("inactive product is never store-eligible")
	}
}

// TestCatalogPolicyWaitsForProduct proves a policy arriving before its core
// product waits retryably, then converges without resend.
func TestCatalogPolicyWaitsForProduct(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 91, "pol2")
	productID := "e0002001-0000-4000-8000-000000000001"
	event := "d0003010-0000-4000-8000-000000000010"
	ingestCatalog(t, env, event, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T10:00:00Z",
		policyPayload(productID, 1, true, false, nil))
	res := projectCatalogOnce(t, env, event)
	if res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
		t.Fatalf("dependency wait: %+v", res)
	}
	if n := saleCount(t, env.pool, "catalog_product_sales_policies"); n != 0 {
		t.Fatalf("zero rows while waiting, got %d", n)
	}
	ingestCatalog(t, env, "d0003011-0000-4000-8000-000000000011", catalog.EventProductSnapshotV2, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-LATE", "متأخر", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0003011-0000-4000-8000-000000000011")
	forceCatalogDue(t, env, catalog.ProcessorProductSalesPolicyProjectionV1, event)
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("policy converges: %+v", res)
	}
}

// TestCatalogPolicyRevisionOrdering proves stale no-ops, equal-revision
// conflicts block, and newer revisions replace atomically.
func TestCatalogPolicyRevisionOrdering(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 92, "pol3")
	productID := "e0002002-0000-4000-8000-000000000002"
	ingestCatalog(t, env, "d0003020-0000-4000-8000-000000000020", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-REV", "توت", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0003020-0000-4000-8000-000000000020")
	ingestCatalog(t, env, "d0003021-0000-4000-8000-000000000021", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 1, true, false, nil))
	projectCatalogOnce(t, env, "d0003021-0000-4000-8000-000000000021")
	// Advance to online+capped at revision 2.
	ingestCatalog(t, env, "d0003022-0000-4000-8000-000000000022", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:02:00Z",
		policyPayload(productID, 2, true, true, policyInt(25)))
	if res := projectCatalogOnce(t, env, "d0003022-0000-4000-8000-000000000022"); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev2: %+v", res)
	}
	// Stale rev 1 redelivery is a no-op.
	ingestCatalog(t, env, "d0003023-0000-4000-8000-000000000023", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:03:00Z",
		policyPayload(productID, 1, true, false, nil))
	if res := projectCatalogOnce(t, env, "d0003023-0000-4000-8000-000000000023"); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale no-op: %+v", res)
	}
	// Equal revision with conflicting state blocks.
	ingestCatalog(t, env, "d0003024-0000-4000-8000-000000000024", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:04:00Z",
		policyPayload(productID, 2, false, true, policyInt(25)))
	if res := projectCatalogOnce(t, env, "d0003024-0000-4000-8000-000000000024"); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("equal-revision conflict: %+v", res)
	}
	svc := catalog.NewService(catalogStore(env))
	policy, err := svc.GetProductSalesPolicy(context.Background(), productID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 2 || !policy.SellOffline || !policy.SellOnline ||
		policy.OnlineAllocationLimit == nil || *policy.OnlineAllocationLimit != 25 {
		t.Fatalf("rev2 intact: %+v", policy)
	}
}

// TestCatalogPolicyOutOfOrderRevisions proves higher revisions win
// regardless of arrival order (highest valid revision converges).
func TestCatalogPolicyOutOfOrderRevisions(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 93, "pol4")
	productID := "e0002003-0000-4000-8000-000000000003"
	ingestCatalog(t, env, "d0003030-0000-4000-8000-000000000030", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-OOO", "توت", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0003030-0000-4000-8000-000000000030")
	ingestCatalog(t, env, "d0003031-0000-4000-8000-000000000031", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 3, false, true, policyInt(7)))
	ingestCatalog(t, env, "d0003032-0000-4000-8000-000000000032", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:02:00Z",
		policyPayload(productID, 2, true, true, policyInt(25)))
	projectCatalogOnce(t, env, "d0003032-0000-4000-8000-000000000032")
	projectCatalogOnce(t, env, "d0003031-0000-4000-8000-000000000031")
	svc := catalog.NewService(catalogStore(env))
	policy, err := svc.GetProductSalesPolicy(context.Background(), productID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 3 || policy.SellOffline || !policy.SellOnline ||
		policy.OnlineAllocationLimit == nil || *policy.OnlineAllocationLimit != 7 {
		t.Fatalf("rev3 wins: %+v", policy)
	}
}

// TestCatalogPolicyInvalidBlocked proves malformed policy is terminal:
// stale caps and bad revisions never project.
func TestCatalogPolicyInvalidBlocked(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 94, "pol5")
	productID := "e0002004-0000-4000-8000-000000000004"
	ingestCatalog(t, env, "d0003040-0000-4000-8000-000000000040", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-BAD", "توت", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0003040-0000-4000-8000-000000000040")
	// Stale cap with online disabled is rejected at ingestion.
	bad := "d0003041-0000-4000-8000-000000000041"
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":"2026-09-20T12:01:00Z","payload":%s}]}`,
		bad, catalog.EventProductSalesPolicySnapshotV1, policyPayload(productID, 2, true, false, policyInt(5)))
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err == nil {
		t.Fatal("stale cap must reject at ingestion")
	}
	if n := saleCount(t, env.pool, "catalog_product_sales_policies"); n != 0 {
		t.Fatalf("nothing projects from invalid policy, got %d", n)
	}
}

// inventoryPayload models the single physical Variant of these fixtures.
func inventoryPayload(productID string, revision int64, stock int) string {
	return variantInventoryPayload(inventoryVariantID(productID), productID, inventoryVariantID(productID), stock, revision, true, nil)
}

func inventoryVariantID(productID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-inventory-variant:"+productID)).String()
}

// seedInventoryVariant is explicit fixture setup, never a production fallback.
func seedInventoryVariant(t *testing.T, env *saleEnv, productID string) {
	t.Helper()
	vid := inventoryVariantID(productID)
	var count int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM catalog_product_variants WHERE variant_id=$1`, vid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		return
	}
	event := uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-inventory-catalog:"+productID)).String()
	ingestCatalog(t, env, event, catalog.EventProductVariantSnapshotV1, "2026-09-20T12:00:00Z", variantPayload(vid, productID, vid, true, false, "", nil, 1))
	if got := projectCatalogOnce(t, env, event); got.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("seed inventory variant: %+v", got)
	}
}

// seedScopeInventoryVariant explicitly projects the physical fixture Variant
// through the parent Product's original device/Store, even for foreign-writer
// negative controls. It does not adopt another Store's catalog state.
func seedScopeInventoryVariant(t *testing.T, f *scopeFixture, productID string) {
	t.Helper()
	vid := inventoryVariantID(productID)
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM catalog_product_variants WHERE variant_id=$1`, vid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		return
	}
	var dev string
	if err := f.pool.QueryRow(context.Background(), `SELECT source_device_id::text FROM catalog_products WHERE product_id=$1`, productID).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	cred := ""
	switch dev {
	case f.devA:
		cred = f.credA
	case f.devB:
		cred = f.credB
	case f.devC:
		cred = f.credC
	default:
		t.Fatal("unknown fixture source device")
	}
	event := uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-scoped-inventory-catalog:"+productID)).String()
	f.ingest(t, dev, cred, event, catalog.EventProductVariantSnapshotV1, variantPayload(vid, productID, vid, true, false, "", nil, 1))
	requireOutcome(t, projectCatalogTerminal(t, f, event, catalog.EventProductVariantSnapshotV1), catalog.OutcomeProcessed, "")
}

func inventoryFixture(t *testing.T, env *saleEnv, base int, prefix, productID, sku string, stock int) {
	t.Helper()
	root, sub, tag := catalogProductFixture(t, env, base, prefix)
	ingestCatalog(t, env, "d0006000-0000-4000-8000-000000000000", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(productID, sku, "مخزون", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0006000-0000-4000-8000-000000000000")
	_ = stock
}

// TestInventoryProjects proves a rev-1 inventory snapshot projects with
// revision metadata and becomes readable with uncapped availability.
func TestInventoryProjects(t *testing.T) {
	env := openSaleEnv(t)
	productID := "e0005000-0000-4000-8000-000000000000"
	inventoryFixture(t, env, 150, "inv1", productID, "PAP-INV", 10)
	event := "d0006010-0000-4000-8000-000000000010"
	seedInventoryVariant(t, env, productID)
	ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:01:00Z",
		inventoryPayload(productID, 1, 10))
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("inventory: %+v", res)
	}
	svc := catalog.NewService(catalogStore(env))
	inv, err := svc.GetVariantInventory(context.Background(), inventoryVariantID(productID))
	if err != nil {
		t.Fatalf("read service: %v", err)
	}
	if inv.StockQuantity != 10 || inv.Revision != 1 {
		t.Fatalf("inventory: %+v", inv)
	}
	// No policy yet: availability is safe-zero and not ready.
	availability, err := svc.GetProductAvailability(context.Background(), productID)
	if err != nil {
		t.Fatal(err)
	}
	if availability.OnlineAvailable != 0 || availability.Ready {
		t.Fatalf("missing policy: %+v", availability)
	}
	if !availability.MissingPolicy || availability.MissingInventory {
		t.Fatalf("missing flags: %+v", availability)
	}
}

// TestInventoryWaitsForProduct proves inventory arriving before its core
// product waits retryably, then converges without resend.
func TestInventoryWaitsForProduct(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 151, "inv2")
	productID := "e0005001-0000-4000-8000-000000000001"
	_ = root
	_ = sub
	_ = tag
	event := "d0006020-0000-4000-8000-000000000020"
	ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		inventoryPayload(productID, 1, 7))
	res := projectCatalogOnce(t, env, event)
	if res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
		t.Fatalf("dependency wait: %+v", res)
	}
	if n := saleCount(t, env.pool, "catalog_product_variant_inventory"); n != 0 {
		t.Fatalf("zero rows while waiting, got %d", n)
	}
	ingestCatalog(t, env, "d0006021-0000-4000-8000-000000000021", catalog.EventProductSnapshotV2, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-LATE", "متأخر", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0006021-0000-4000-8000-000000000021")
	seedInventoryVariant(t, env, productID)
	forceCatalogDue(t, env, catalog.ProcessorProductVariantInventoryProjectionV1, event)
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("inventory converges: %+v", res)
	}
}

// TestInventoryEqualRevision proves identical redelivery is idempotent
// while conflicting quantity at the same revision blocks, and a higher
// revision repairs afterwards.
func TestInventoryEqualRevision(t *testing.T) {
	env := openSaleEnv(t)
	productID := "e0005003-0000-4000-8000-000000000003"
	inventoryFixture(t, env, 153, "inv4", productID, "PAP-EQ", 10)
	ingest := func(event string, revision int64, stock int) catalog.ProjectResult {
		seedInventoryVariant(t, env, productID)
		ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:00:00Z",
			inventoryPayload(productID, revision, stock))
		return projectCatalogOnce(t, env, event)
	}
	if res := ingest("d0006040-0000-4000-8000-000000000040", 5, 10); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev5: %+v", res)
	}
	// Same revision, same state, new event ID: idempotent.
	if res := ingest("d0006041-0000-4000-8000-000000000041", 5, 10); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("identical redelivery: %+v", res)
	}
	// Same revision, different quantity: semantic conflict, state kept.
	if res := ingest("d0006042-0000-4000-8000-000000000042", 5, 11); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("equal-revision conflict: %+v", res)
	}
	svc := catalog.NewService(catalogStore(env))
	inv, err := svc.GetVariantInventory(context.Background(), inventoryVariantID(productID))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Revision != 5 || inv.StockQuantity != 10 {
		t.Fatalf("state preserved: %+v", inv)
	}
	// Higher revision repairs.
	if res := ingest("d0006043-0000-4000-8000-000000000043", 6, 4); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev6 repair: %+v", res)
	}
	inv, err = svc.GetVariantInventory(context.Background(), inventoryVariantID(productID))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Revision != 6 || inv.StockQuantity != 4 {
		t.Fatalf("repaired: %+v", inv)
	}
}

// TestInventoryConcurrentRevisionsDeterministic forces rev5 vs rev6 through
// competing transactions: the final state must be revision 6 quantity 14.
func TestInventoryConcurrentRevisionsDeterministic(t *testing.T) {
	env := openSaleEnv(t)
	productID := "e0005004-0000-4000-8000-000000000004"
	inventoryFixture(t, env, 154, "inv5", productID, "PAP-CONC", 10)
	rev := func(revision int64, stock int, num string) string {
		event := fmt.Sprintf("d00060%s-0000-4000-8000-0000000000%s", num, num)
		seedInventoryVariant(t, env, productID)
		ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:00:00Z",
			inventoryPayload(productID, revision, stock))
		return event
	}
	ev5 := rev(5, 15, "50")
	ev6 := rev(6, 14, "60")

	start := make(chan struct{})
	done := make(chan catalogWorkerResult, 2)
	for _, event := range []string{ev5, ev6} {
		go func(event string) {
			<-start
			done <- catalogWorkerAttempt(catalogStore(env), event, catalog.EventInventoryProductVariantSnapshotV1)
		}(event)
	}
	close(start)
	results := []catalogWorkerResult{<-done, <-done}
	for _, result := range results {
		if result.Err != nil && !catalogAttemptRetryable(result.Result, result.Err) {
			t.Fatalf("inventory worker: %v", result.Err)
		}
	}
	for _, event := range []string{ev5, ev6} {
		driveCatalogToTerminal(t, env, event, 10)
	}
	_ = results

	svc := catalog.NewService(catalogStore(env))
	inv, err := svc.GetVariantInventory(context.Background(), inventoryVariantID(productID))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Revision != 6 || inv.StockQuantity != 14 {
		t.Fatalf("newest revision wins: %+v", inv)
	}
}

// TestInventoryProjectionRollback faults the upsert so the old revision
// stays visible and retryable; after repair, retry commits exactly once.
func TestInventoryProjectionRollback(t *testing.T) {
	env := openSaleEnv(t)
	productID := "e0005005-0000-4000-8000-000000000005"
	inventoryFixture(t, env, 155, "inv6", productID, "PAP-RB2", 10)
	seedInventoryVariant(t, env, productID)
	ingestCatalog(t, env, "d0006060-0000-4000-8000-000000000060", catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:00:00Z",
		inventoryPayload(productID, 1, 10))
	projectCatalogOnce(t, env, "d0006060-0000-4000-8000-000000000060")

	if _, err := env.pool.Exec(context.Background(),
		`CREATE OR REPLACE FUNCTION block_inventory_upsert() RETURNS trigger LANGUAGE plpgsql AS
		 $$BEGIN RAISE EXCEPTION 'forced inventory upsert failure'; END$$`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`CREATE TRIGGER fault_inventory_upsert BEFORE INSERT OR UPDATE ON catalog_product_variant_inventory
		 FOR EACH ROW EXECUTE FUNCTION block_inventory_upsert()`); err != nil {
		t.Fatal(err)
	}
	event := "d0006061-0000-4000-8000-000000000061"
	seedInventoryVariant(t, env, productID)
	ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:01:00Z",
		inventoryPayload(productID, 2, 6))
	res := projectCatalogOnce(t, env, event)
	if res.Outcome != catalog.OutcomeRetryable {
		t.Fatalf("fault retryable: %+v", res)
	}
	svc := catalog.NewService(catalogStore(env))
	inv, err := svc.GetVariantInventory(context.Background(), inventoryVariantID(productID))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Revision != 1 || inv.StockQuantity != 10 {
		t.Fatalf("old revision visible: %+v", inv)
	}
	if _, err := env.pool.Exec(context.Background(), `DROP TRIGGER fault_inventory_upsert ON catalog_product_variant_inventory`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(), `DROP FUNCTION block_inventory_upsert()`); err != nil {
		t.Fatal(err)
	}
	forceCatalogDue(t, env, catalog.ProcessorProductVariantInventoryProjectionV1, event)
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("retry commits: %+v", res)
	}
	inv, err = svc.GetVariantInventory(context.Background(), inventoryVariantID(productID))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Revision != 2 || inv.StockQuantity != 6 {
		t.Fatalf("exactly once: %+v", inv)
	}
}

// TestInventoryAvailabilityMatrix drives availability through the real
// read service across lifecycle, policy, stock, and cap combinations.
func TestInventoryAvailabilityMatrix(t *testing.T) {
	env := openSaleEnv(t)
	svc := catalog.NewService(catalogStore(env))
	ctx := context.Background()

	setup := func(prefix string, base int, active, sellOnline bool, cap *int, stock int) string {
		ids := catalogIDs(t, base, prefix+"-root", prefix+"-sub", prefix+"-tag")
		root, sub, tag := ids[prefix+"-root"], ids[prefix+"-sub"], ids[prefix+"-tag"]
		ce := func(n int) string { return fmt.Sprintf("d%07d-0000-4000-8000-100000000%03d", base, n) }
		ingestCatalog(t, env, ce(1), catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
			categoryPayload(root, "active", map[string]string{"ar": "جذر"}, nil, 1))
		projectCatalogOnce(t, env, ce(1))
		ingestCatalog(t, env, ce(2), catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
			categoryPayload(sub, "active", map[string]string{"ar": "فرعي"}, []string{root}, 1))
		projectCatalogOnce(t, env, ce(2))
		ingestCatalog(t, env, ce(3), catalog.EventTagSnapshotV1, "2026-09-20T10:00:00Z",
			tagPayload(tag, "avm-"+prefix, true, map[string]string{"en": "Tag"}, 1))
		projectCatalogOnce(t, env, ce(3))
		productID := fmt.Sprintf("e%07d-0000-4000-8000-000000000000", base)
		payload := productPayload(productID, "PAP-"+prefix, "x", root, []string{sub}, []string{tag}, 1)
		if !active {
			var m map[string]any
			if err := json.Unmarshal([]byte(payload), &m); err != nil {
				t.Fatal(err)
			}
			m["is_active"] = false
			raw, _ := json.Marshal(m)
			payload = string(raw)
		}
		pe := fmt.Sprintf("d%07d-0000-4000-8000-000000000001", base)
		ingestCatalog(t, env, pe, catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z", payload)
		projectCatalogOnce(t, env, pe)
		oe := fmt.Sprintf("d%07d-0000-4000-8000-000000000002", base)
		ingestCatalog(t, env, oe, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
			policyPayload(productID, 1, true, sellOnline, cap))
		projectCatalogOnce(t, env, oe)
		ie := fmt.Sprintf("d%07d-0000-4000-8000-000000000003", base)
		seedInventoryVariant(t, env, productID)
		ingestCatalog(t, env, ie, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:02:00Z",
			inventoryPayload(productID, 1, stock))
		projectCatalogOnce(t, env, ie)
		return productID
	}
	cap3 := 3
	cap0 := 0
	// Bases step by 10: catalogIDs mints base..base+2, so consecutive
	// bases would overlap entity IDs across setups.
	uncapped := setup("avm1", 160, true, true, nil, 10)
	capped := setup("avm2", 170, true, true, &cap3, 10)
	cappedAbove := setup("avm3", 180, true, true, &cap3, 2)
	zeroCap := setup("avm4", 190, true, true, &cap0, 10)
	offline := setup("avm5", 200, true, false, nil, 10)
	inactive := setup("avm6", 210, false, true, nil, 10)

	cases := []struct {
		name       string
		id         string
		wantOnline int
		wantReady  bool
	}{
		{"uncapped 10", uncapped, 10, true},
		{"cap 3 of 10", capped, 3, true},
		{"cap 3 of 2", cappedAbove, 2, true},
		{"zero cap", zeroCap, 0, true},
		{"online disabled", offline, 0, true},
		{"inactive", inactive, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			availability, err := svc.GetProductAvailability(ctx, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if availability.OnlineAvailable != tc.wantOnline || availability.Ready != tc.wantReady {
				t.Fatalf("got (%d,%v): %+v", availability.OnlineAvailable, availability.Ready, availability)
			}
		})
	}
	// Inactive keeps its inventory truth even though nothing is sellable.
	if a, err := svc.GetProductAvailability(ctx, inactive); err != nil || a.StockQuantity == nil || *a.StockQuantity != 10 {
		t.Fatalf("inactive keeps inventory truth: %+v %v", a, err)
	}

	// Missing inventory: policy present, no inventory row → 0 + not ready.
	root, sub, tag := catalogProductFixture(t, env, 220, "avm7")
	noInv := "e0005166-0000-4000-8000-000000000000"
	ingestCatalog(t, env, "d0006160-0000-4000-8000-000000000060", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(noInv, "PAP-NOINV", "x", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0006160-0000-4000-8000-000000000060")
	ingestCatalog(t, env, "d0006161-0000-4000-8000-000000000061", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(noInv, 1, true, true, nil))
	projectCatalogOnce(t, env, "d0006161-0000-4000-8000-000000000061")
	availability, err := svc.GetProductAvailability(ctx, noInv)
	if err != nil {
		t.Fatal(err)
	}
	if availability.OnlineAvailable != 0 || availability.Ready || !availability.MissingInventory {
		t.Fatalf("missing inventory: %+v", availability)
	}

	// Unknown product: zero availability, not ready.
	availability, err = svc.GetProductAvailability(ctx, "e9999999-0000-4000-8000-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if availability.OnlineAvailable != 0 || availability.Ready {
		t.Fatalf("unknown product: %+v", availability)
	}
}

// TestInventoryAvailabilityFollowsChanges proves availability recomputes
// from current state without companion revisions: deactivation zeroes it,
// a policy cap change narrows it, a stock change moves it.
func TestInventoryAvailabilityFollowsChanges(t *testing.T) {
	env := openSaleEnv(t)
	svc := catalog.NewService(catalogStore(env))
	ctx := context.Background()
	root, sub, tag := catalogProductFixture(t, env, 167, "avf")
	productID := "e0005167-0000-4000-8000-000000000000"
	ingestCatalog(t, env, "d0006170-0000-4000-8000-000000000070", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-FLW", "x", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0006170-0000-4000-8000-000000000070")
	ingestCatalog(t, env, "d0006171-0000-4000-8000-000000000071", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 1, true, true, nil))
	projectCatalogOnce(t, env, "d0006171-0000-4000-8000-000000000071")
	seedInventoryVariant(t, env, productID)
	ingestCatalog(t, env, "d0006172-0000-4000-8000-000000000072", catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:02:00Z",
		inventoryPayload(productID, 1, 10))
	projectCatalogOnce(t, env, "d0006172-0000-4000-8000-000000000072")

	availability, err := svc.GetProductAvailability(ctx, productID)
	if err != nil {
		t.Fatal(err)
	}
	if availability.OnlineAvailable != 10 {
		t.Fatalf("baseline: %+v", availability)
	}

	// Policy cap 4 at policy rev2, no inventory change: availability 4.
	ingestCatalog(t, env, "d0006173-0000-4000-8000-000000000073", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:03:00Z",
		policyPayload(productID, 2, true, true, func() *int { v := 4; return &v }()))
	projectCatalogOnce(t, env, "d0006173-0000-4000-8000-000000000073")
	availability, err = svc.GetProductAvailability(ctx, productID)
	if err != nil {
		t.Fatal(err)
	}
	if availability.OnlineAvailable != 4 {
		t.Fatalf("cap change: %+v", availability)
	}

	// Stock 10 → 3 at inventory rev2, no policy change: availability 3.
	seedInventoryVariant(t, env, productID)
	ingestCatalog(t, env, "d0006174-0000-4000-8000-000000000074", catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:04:00Z",
		inventoryPayload(productID, 2, 3))
	projectCatalogOnce(t, env, "d0006174-0000-4000-8000-000000000074")
	availability, err = svc.GetProductAvailability(ctx, productID)
	if err != nil {
		t.Fatal(err)
	}
	if availability.OnlineAvailable != 3 {
		t.Fatalf("stock change: %+v", availability)
	}

	// Deactivate at catalog rev2, no other change: availability 0.
	deactivated := productPayload(productID, "PAP-FLW", "x", root, []string{sub}, []string{tag}, 2)
	var deactivatedMap map[string]any
	if err := json.Unmarshal([]byte(deactivated), &deactivatedMap); err != nil {
		t.Fatal(err)
	}
	deactivatedMap["is_active"] = false
	deactivatedRaw, _ := json.Marshal(deactivatedMap)
	ingestCatalog(t, env, "d0006175-0000-4000-8000-000000000075", catalog.EventProductSnapshotV2, "2026-09-20T12:05:00Z", string(deactivatedRaw))
	projectCatalogOnce(t, env, "d0006175-0000-4000-8000-000000000075")
	availability, err = svc.GetProductAvailability(ctx, productID)
	if err != nil {
		t.Fatal(err)
	}
	if availability.OnlineAvailable != 0 || !availability.Ready {
		t.Fatalf("deactivation: %+v", availability)
	}
}

// TestInventoryRebuild proves accepted inventory history rebuilds to the
// highest valid revision with conflict behavior preserved.
func TestInventoryRebuild(t *testing.T) {
	env := openSaleEnv(t)
	productID := "e0005007-0000-4000-8000-000000000007"
	inventoryFixture(t, env, 168, "inv8", productID, "PAP-RBD", 10)
	ingest := func(event string, revision int64, stock int) catalog.ProjectResult {
		seedInventoryVariant(t, env, productID)
		ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:00:00Z",
			inventoryPayload(productID, revision, stock))
		return projectCatalogOnce(t, env, event)
	}
	if res := ingest("d0006080-0000-4000-8000-000000000080", 2, 9); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev2: %+v", res)
	}
	if res := ingest("d0006081-0000-4000-8000-000000000081", 5, 7); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev5: %+v", res)
	}
	if res := ingest("d0006082-0000-4000-8000-000000000082", 3, 8); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale rev3: %+v", res)
	}
	if res := ingest("d0006083-0000-4000-8000-000000000083", 5, 99); res.Outcome != catalog.OutcomeBlocked {
		t.Fatalf("rev5 conflict: %+v", res)
	}

	before := dumpCatalog(t, env)
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `DELETE FROM catalog_product_variant_inventory`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
		 attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
		 WHERE processor = 'inventory_product_variant_projection.v1'`); err != nil {
		t.Fatal(err)
	}
	rows, err := env.pool.Query(ctx,
		`SELECT event_id::text FROM sync_events WHERE event_type = 'inventory.product_variant.snapshot.v1' ORDER BY received_at ASC`)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		order = append(order, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	store := catalogStore(env)
	for _, id := range order {
		rec, ok, err := store.LoadCatalogEvent(ctx, id)
		if err != nil || !ok {
			t.Fatal("load")
		}
		if _, err := store.ProjectProductVariantInventory(ctx, rec, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	drainCatalog(t, env)
	if after := dumpCatalog(t, env); after != before {
		t.Fatalf("rebuild mismatch:\nbefore: %s\nafter:  %s", before, after)
	}
	// The equal-revision conflict re-blocks deterministically in original
	// order; the highest valid revision is authoritative.
	if status, code := catalogStatus(t, env, catalog.ProcessorProductVariantInventoryProjectionV1, "d0006083-0000-4000-8000-000000000083"); status != "blocked" || code != ErrCatalogRevisionConflict {
		t.Fatalf("conflict re-blocked: %q/%q", status, code)
	}
	svc := catalog.NewService(catalogStore(env))
	inv, err := svc.GetVariantInventory(context.Background(), inventoryVariantID(productID))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Revision != 5 || inv.StockQuantity != 7 {
		t.Fatalf("highest valid wins: %+v", inv)
	}
}

// TestCatalogMixedRevisionKeys proves the shared entity-revision scan
// reads each stream's own revision column: catalog 12/13, policy 4/5,
// inventory 27/28. A wrong key fails closed instead of silently
// cross-reading another stream's revisions.
func TestCatalogMixedRevisionKeys(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 230, "mix")
	productID := "e0005230-0000-4000-8000-000000000000"
	ingestCatalog(t, env, "d0006230-0000-4000-8000-000000000030", catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-MIX", "x", root, []string{sub}, []string{tag}, 12))
	projectCatalogOnce(t, env, "d0006230-0000-4000-8000-000000000030")
	ingestCatalog(t, env, "d0006231-0000-4000-8000-000000000031", catalog.EventProductSnapshotV2, "2026-09-20T12:01:00Z",
		productPayload(productID, "PAP-MIX", "x", root, []string{sub}, []string{tag}, 13))
	ingestCatalog(t, env, "d0006232-0000-4000-8000-000000000032", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:02:00Z",
		policyPayload(productID, 4, true, true, policyInt(9)))
	projectCatalogOnce(t, env, "d0006232-0000-4000-8000-000000000032")
	ingestCatalog(t, env, "d0006233-0000-4000-8000-000000000033", catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:03:00Z",
		policyPayload(productID, 5, true, true, policyInt(9)))
	seedInventoryVariant(t, env, productID)
	ingestCatalog(t, env, "d0006234-0000-4000-8000-000000000034", catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:04:00Z",
		inventoryPayload(productID, 27, 6))
	projectCatalogOnce(t, env, "d0006234-0000-4000-8000-000000000034")
	seedInventoryVariant(t, env, productID)
	ingestCatalog(t, env, "d0006235-0000-4000-8000-000000000035", catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T12:05:00Z",
		inventoryPayload(productID, 28, 5))

	ctx := context.Background()
	store := catalogStore(env)
	q := sqlcgen.New(env.pool)
	_ = store
	revisions := func(eventType, revKey string) []int64 {
		t.Helper()
		rows, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{
			EventType: eventType, Column2: "product_id", Column3: productID,
			Processor: "test", Column5: revKey,
		})
		if err != nil {
			t.Fatalf("%s/%s: %v", eventType, revKey, err)
		}
		var out []int64
		for _, row := range rows {
			out = append(out, row.Revision)
		}
		return out
	}
	assertEqualInt64s(t, "catalog", revisions(catalog.EventProductSnapshotV2, "catalog_revision"), []int64{12, 13})
	assertEqualInt64s(t, "policy", revisions(catalog.EventProductSalesPolicySnapshotV1, "sales_policy_revision"), []int64{4, 5})
	assertEqualInt64s(t, "inventory", revisions(catalog.EventInventoryProductVariantSnapshotV1, "inventory_revision"), []int64{27, 28})

	// A missing revision key fails closed instead of casting a guessed value.
	if _, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{
		EventType: catalog.EventInventoryProductVariantSnapshotV1, Column2: "product_id", Column3: productID,
		Processor: "test", Column5: "nonexistent_revision",
	}); err == nil {
		t.Fatal("wrong revision key must fail, not cross-read")
	}
}

func assertEqualInt64s(t *testing.T, what string, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	seen := map[int64]int{}
	for _, v := range got {
		seen[v]++
	}
	for _, v := range want {
		seen[v]--
		if seen[v] < 0 {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
	}
}
