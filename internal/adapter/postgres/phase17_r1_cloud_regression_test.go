package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/google/uuid"
)

func phase17CreateTypeIntent(t *testing.T, r remediationCloud, code string) catalogadmin.CommandView {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"code": code, "name_ar": "كتاب", "name_en": "Book",
		"dimensions": []string{}, "capabilities": []string{}, "expected_type_revision": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.s.Create(context.Background(), "review", r.sid, catalogadmin.TypeProductTypeCreateV1, "", 0, payload)
	if err != nil || len(v.Targets) != 1 {
		t.Fatalf("create intent: %+v %v", v, err)
	}
	if _, err := r.s.Poll(context.Background(), r.did, 10); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPhase17CreateOutcomeAtomicResultAndReplayRepair(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	v := phase17CreateTypeIntent(t, r, "atomic_book")
	resultID := uuid.NewString()
	installFault := func() {
		t.Helper()
		_, err := r.p.Exec(ctx, `CREATE OR REPLACE FUNCTION phase17_result_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected result failure'; END $$;
			CREATE TRIGGER phase17_result_failure BEFORE UPDATE OF result_entity_id ON catalog_admin_commands FOR EACH ROW EXECUTE FUNCTION phase17_result_failure()`)
		if err != nil {
			t.Fatal(err)
		}
	}
	removeFault := func() {
		t.Helper()
		if _, err := r.p.Exec(ctx, `DROP TRIGGER phase17_result_failure ON catalog_admin_commands`); err != nil {
			t.Fatal(err)
		}
	}
	ack := func() error {
		return r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, catalogadmin.TargetApplied, catalogadmin.CodeApplied, resultID, 0, 1)
	}
	assertState := func(status, result string) {
		t.Helper()
		got, err := r.s.Get(ctx, r.sid, v.ID)
		if err != nil || got.Targets[0].Status != status || got.ResultEntityID != result {
			t.Fatalf("durable outcome: %+v %v", got, err)
		}
	}
	installFault()
	if err := ack(); err == nil {
		t.Fatal("result persistence fault acknowledged as success")
	}
	assertState(catalogadmin.TargetDelivered, "")
	removeFault()
	if err := ack(); err != nil {
		t.Fatal(err)
	}
	assertState(catalogadmin.TargetApplied, resultID)
	// Simulate the old split-commit crash state. An identical durable ACK
	// must repair missing parent identity without reapplying business work.
	if _, err := r.p.Exec(ctx, `UPDATE catalog_admin_commands SET result_entity_id=NULL WHERE id=$1`, v.ID); err != nil {
		t.Fatal(err)
	}
	installFault()
	if err := ack(); err == nil {
		t.Fatal("failed replay repair acknowledged as success")
	}
	assertState(catalogadmin.TargetApplied, "")
	removeFault()
	if err := ack(); err != nil {
		t.Fatal(err)
	}
	assertState(catalogadmin.TargetApplied, resultID)
	// Reconstruct the service from durable state; result evidence survives.
	reopened := catalogadmin.NewService(r.d, r.dir)
	got, err := reopened.Get(ctx, r.sid, v.ID)
	if err != nil || got.ResultEntityID != resultID {
		t.Fatalf("reopened result evidence: %+v %v", got, err)
	}
	if err := r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, catalogadmin.TargetApplied, catalogadmin.CodeApplied, uuid.NewString(), 0, 1); err == nil {
		t.Fatal("contradictory result identity accepted")
	}
	assertState(catalogadmin.TargetApplied, resultID)
	contradictory := uuid.NewString()
	if _, err := r.p.Exec(ctx, `UPDATE catalog_admin_commands SET result_entity_id=$1 WHERE id=$2`, contradictory, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := ack(); err == nil {
		t.Fatal("replay overwrote contradictory durable parent identity")
	}
	assertState(catalogadmin.TargetApplied, contradictory)
}

func TestPhase17FailedCreateHasNoResultIdentity(t *testing.T) {
	for _, tc := range []struct{ status, code string }{
		{catalogadmin.TargetConflict, catalogadmin.CodeConflict},
		{catalogadmin.TargetRejected, catalogadmin.CodeValidationFailed},
	} {
		t.Run(tc.status, func(t *testing.T) {
			r := remediationCloudSetup(t)
			ctx := context.Background()
			v := phase17CreateTypeIntent(t, r, "failed_book")
			tid := v.Targets[0].ID
			// Failure did not mint an entity; claiming an existing or fake
			// UUID, a revision advance, or an arbitrary code remains invalid.
			for _, bad := range []struct {
				entity, code string
				pre, post    int64
			}{{uuid.NewString(), tc.code, 0, 0}, {"", tc.code, 0, 1}, {"", tc.code, -1, 0}, {"", "RAW_ERROR", 0, 0}} {
				if err := r.s.ReportOutcome(ctx, r.did, tid, tc.status, bad.code, bad.entity, bad.pre, bad.post); err == nil {
					t.Fatalf("invalid failed-create outcome accepted: %+v", bad)
				}
			}
			if err := r.s.ReportOutcome(ctx, r.did, tid, tc.status, tc.code, "", 0, 0); err != nil {
				t.Fatalf("genuine no-entity outcome rejected: %v", err)
			}
			if err := r.s.ReportOutcome(ctx, r.did, tid, tc.status, tc.code, "", 0, 0); err != nil {
				t.Fatalf("identical failure replay rejected: %v", err)
			}
			got, err := r.s.Get(ctx, r.sid, v.ID)
			if err != nil || got.Targets[0].Status != tc.status || got.Targets[0].EntityID != "" || got.ResultEntityID != "" || got.Converged {
				t.Fatalf("failed-create state: %+v %v", got, err)
			}
		})
	}
}

// Project genuine v3 payloads so financial queries consume immutable Type
// snapshots through the normal ingestion/projection boundary.
func phase17TypeSale(t *testing.T, env *saleEnv, fixtureName, saleID, occurred, name string) (map[string]any, map[string]any) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(fixture(t, fixtureName)), &payload); err != nil {
		t.Fatal(err)
	}
	payload["sale_id"], payload["occurred_at"], payload["paid_at"] = saleID, occurred, occurred
	for _, entry := range payload["lines"].([]any) {
		line := entry.(map[string]any)
		line["tags"] = []any{}
		line["cost"] = map[string]any{"amount_minor": 400, "currency": payload["currency"]}
		line["variant_id"], line["variant_sku"], line["variant_attributes"] = uuid.NewString(), line["sku"], []any{}
		line["product_type_id"], line["product_type_code"] = "10000000-0000-4000-8000-000000000001", "book"
		line["product_type_name_ar"], line["product_type_name_en"] = "كتاب "+name, name
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	eid := uuid.NewString()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v3","occurred_at":%q,"payload":%s}]}`, eid, occurred, encoded)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil || res.Events[0].Status != "accepted" {
		t.Fatalf("v3 ingestion: %+v %v", res, err)
	}
	d := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := d.LoadSaleEvent(context.Background(), eid)
	if err != nil || !ok {
		t.Fatalf("load v3: %v %v", ok, err)
	}
	projected, err := d.ProjectSaleV3(context.Background(), rec, time.Now())
	if err != nil || projected.Outcome != sale.OutcomeProcessed {
		t.Fatalf("v3 projection: %+v %v", projected, err)
	}
	return payload, payload["lines"].([]any)[0].(map[string]any)
}

func TestPhase17ProductTypeHistoricalRefundsAndStoreScope(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	a, b := uuid.NewString(), uuid.NewString()
	for _, sid := range []string{a, b} {
		if _, err := env.pool.Exec(ctx, `INSERT INTO stores(id,display_name,timezone) VALUES($1,'Report','Africa/Cairo')`, sid); err != nil {
			t.Fatal(err)
		}
	}
	oldID, newID, foreignID, usdID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	old, line := phase17TypeSale(t, env, "sale_egp.json", oldID, "2026-09-20T10:00:00Z", "Old")
	phase17TypeSale(t, env, "sale_egp.json", newID, "2026-09-20T11:00:00Z", "New")
	phase17TypeSale(t, env, "sale_egp.json", foreignID, "2026-09-20T12:00:00Z", "Foreign")
	phase17TypeSale(t, env, "sale_usd.json", usdID, "2026-09-20T13:00:00Z", "Old")
	projectReturnSync(t, env, oldID, old["sale_number"].(string), "EGP", nil, old["shop"].(map[string]any),
		line["sale_item_id"].(string), strPtrOf(line["product_id"]), 1, 100000, 0, 0, 100000, int64Ptr(400),
		"2026-09-21T12:00:00Z", "return", "other", "TYPE-OLD")
	projectReturnSync(t, env, newID, old["sale_number"].(string), "EGP", nil, old["shop"].(map[string]any),
		line["sale_item_id"].(string), strPtrOf(line["product_id"]), 2, 200000, 0, 0, 200000, int64Ptr(800),
		"2026-09-20T14:00:00Z", "return", "other", "TYPE-NEW")
	// Explicit fixture ownership exercises both canonical Store query
	// variants without changing historical Type snapshots.
	if _, err := env.pool.Exec(ctx, `UPDATE sales_projection SET store_id=$1 WHERE sale_id<>$2`, a, foreignID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE return_refund_projection SET store_id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE sales_projection SET store_id=$1 WHERE sale_id=$2`, b, foreignID); err != nil {
		t.Fatal(err)
	}
	svc := repService(env)
	req, err := svc.ParseRequest("custom", "2026-09-20", "2026-09-21", "EGP")
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := report.ParseStoreScope(a)
	if err != nil {
		t.Fatal(err)
	}
	req.Store = scoped
	out, err := svc.Breakdown(ctx, req, report.DimensionProductType)
	if err != nil || len(out.Rows) != 2 {
		t.Fatalf("Store A historical Type buckets: %+v %v", out, err)
	}
	want := map[string]struct{ units, returned, gross, refund, cost, returnedCost int64 }{
		"Old": {2, 1, 200000, 100000, 800, 400}, "New": {2, 2, 200000, 200000, 800, 800},
	}
	for _, row := range out.Rows {
		w, ok := want[*row.ProductTypeNameEN]
		if !ok || len(row.LineSales) != 1 {
			t.Fatalf("unexpected historical row: %+v", row)
		}
		x := row.LineSales[0]
		if row.Units != w.units || row.UnitsReturned != w.returned || x.LineSalesMinor != w.gross || x.LineRefundMinor != w.refund || x.LineCostMinor != w.cost || x.LineReturnedCostMinor != w.returnedCost {
			t.Fatalf("historical economics mismatch: %+v %+v", row, x)
		}
	}
	if *out.Rows[0].ProductTypeNameEN != "Old" {
		t.Fatal("native net ranking ignored refunds", out.Rows)
	}
	oneDay, err := svc.ParseRequest("custom", "2026-09-21", "2026-09-21", "EGP")
	if err != nil {
		t.Fatal(err)
	}
	oneDay.Store = scoped
	negative, err := svc.Breakdown(ctx, oneDay, report.DimensionProductType)
	if err != nil || len(negative.Rows) != 1 || negative.Rows[0].Units != 0 || negative.Rows[0].UnitsReturned != 1 || negative.Rows[0].LineSales[0].LineSalesMinor-negative.Rows[0].LineSales[0].LineRefundMinor != -100000 {
		t.Fatalf("refund-only negative net: %+v %v", negative, err)
	}
	req.Store, err = report.ParseStoreScope(b)
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Breakdown(ctx, req, report.DimensionProductType)
	if err != nil || len(other.Rows) != 1 || *other.Rows[0].ProductTypeNameEN != "Foreign" || other.Rows[0].LineSales[0].LineRefundMinor != 0 {
		t.Fatalf("Store B isolation: %+v %v", other, err)
	}
	req.Store, req.Currency = scoped, ""
	mixed, err := svc.Breakdown(ctx, req, report.DimensionProductType)
	if err != nil || len(mixed.Rows) != 2 {
		t.Fatalf("mixed currency: %+v %v", mixed, err)
	}
	for _, row := range mixed.Rows {
		if *row.ProductTypeNameEN == "Old" && (len(row.LineSales) != 2 || row.LineSales[0].Currency != "EGP" || row.LineSales[1].Currency != "USD" || row.LineSales[1].LineSalesMinor != 1300) {
			t.Fatalf("currencies summed or lost: %+v", row)
		}
	}
}
