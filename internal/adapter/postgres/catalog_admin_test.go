package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
)

// stubAdminDevices binds delivery gating to the test's Store.
type stubAdminDevices struct {
	storeID string
	active  map[string]bool
}

func (s *stubAdminDevices) BindingStore(_ context.Context, _ string) (string, error) {
	return s.storeID, nil
}

func (s *stubAdminDevices) DeviceActive(_ context.Context, deviceID string) (bool, error) {
	return s.active[deviceID], nil
}

func (s *stubAdminDevices) DeviceName(_ context.Context, _ string) string { return "e2e-dev" }

// TestCatalogAdminChainEndToEnd runs the full Cloud chain on real
// PostgreSQL: seed Store + device + binding + product projection,
// create (ownership verified against the real projection), poll as
// the device, report APPLIED, aggregate APPLIED-but-not-converged,
// then project the resulting revision and observe CONVERGED.
// Skips without TEST_DATABASE_URL.
func TestCatalogAdminChainEndToEnd(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	store := NewDevices(pool, 5*time.Second)

	prov, err := authSvc.Create(ctx, "e2e-dev")
	if err != nil {
		t.Fatal(err)
	}
	deviceID := prov.Device.ID
	storeID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO stores (id, display_name, timezone) VALUES ($1, 'E2E', 'Africa/Cairo')`, storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_store_bindings (device_id, store_id) VALUES ($1, $2)`, deviceID, storeID); err != nil {
		t.Fatal(err)
	}
	categoryID := uuid.NewString()
	productID := uuid.NewString()
	eventID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash, store_id)
		VALUES ($1, $2, 'catalog.product.snapshot.v1', now(), '{}', '\x00', $3)`, eventID, deviceID, storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		VALUES ($1, 'active', 'جذر', 1, $2, $3, '\x00', now(), $4)`, categoryID, eventID, deviceID, storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO catalog_products (product_id, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		VALUES ($1, 'منتج', $2, TRUE, 7, $3, $4, '\x00', now(), $5)`,
		productID, categoryID, eventID, deviceID, storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO catalog_product_sales_policies (product_id, sell_offline, sell_online, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ($1, TRUE, TRUE, 4, $2, $3, '\x00', now())`, productID, eventID, deviceID); err != nil {
		t.Fatal(err)
	}

	devices := &stubAdminDevices{storeID: storeID, active: map[string]bool{deviceID: true}}
	svc := catalogadmin.NewService(store, devices)

	payload, _ := json.Marshal(map[string]any{"product_id": productID, "arabic_name": "x"})
	view, err := svc.Create(ctx, "op", storeID, catalogadmin.TypeProductDetailsUpdateV1, productID, 7, payload)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.Aggregate != catalogadmin.AggregatePending || len(view.Targets) != 1 {
		t.Fatalf("created: %+v", view)
	}
	// Cross-Store creation against the same projection row must fail.
	if _, err := svc.Create(ctx, "op", uuid.NewString(), catalogadmin.TypeProductDetailsUpdateV1, productID, 7, payload); err == nil {
		t.Fatal("cross-store create must fail")
	}

	// Capability gating: no delivery before announcement.
	due, err := svc.Poll(ctx, deviceID, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("pre-capability poll: %+v %v", due, err)
	}
	if _, err := svc.ReportCapabilities(ctx, deviceID, []string{catalogadmin.CapabilityV1}); err != nil {
		t.Fatal(err)
	}
	due, err = svc.Poll(ctx, deviceID, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("poll: %+v %v", due, err)
	}
	wire := due[0]
	if string(wire.Payload) == "" || wire.PayloadHash == "" || wire.StoreID != storeID {
		t.Fatalf("wire: %+v", wire)
	}
	// Second poll marks delivered but does not duplicate.
	dueAgain, err := svc.Poll(ctx, deviceID, 10)
	if err != nil || len(dueAgain) != 1 {
		t.Fatalf("redelivery poll: %+v %v", dueAgain, err)
	}

	// Retail applies revision 7 -> 8 and reports.
	if err := svc.ReportOutcome(ctx, deviceID, wire.TargetID, catalogadmin.TargetApplied, catalogadmin.CodeApplied, productID, 7, 8); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, storeID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Aggregate != catalogadmin.AggregateApplied || got.Converged {
		t.Fatalf("applied, projection still rev 7: %s converged=%v", got.Aggregate, got.Converged)
	}
	// Normal sync projects revision 8: convergence observed, never fabricated.
	if _, err := pool.Exec(ctx, `UPDATE catalog_products SET source_revision = 8 WHERE product_id = $1`, productID); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get(ctx, storeID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Aggregate != catalogadmin.AggregateConverged || !got.Converged {
		t.Fatalf("converged: %s %v", got.Aggregate, got.Converged)
	}
	// Cancel after apply is refused.
	if _, err := svc.Cancel(ctx, storeID, view.ID); err == nil {
		t.Fatal("cancel after apply must fail")
	}
	// Wrong-device outcome rejected.
	if err := svc.ReportOutcome(ctx, uuid.NewString(), wire.TargetID, catalogadmin.TargetApplied, catalogadmin.CodeApplied, productID, 7, 8); err == nil {
		t.Fatal("wrong-device outcome must fail")
	}
}
