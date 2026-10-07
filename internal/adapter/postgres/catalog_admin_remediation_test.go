package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
)

type remediationDirectory struct {
	d Devices
	a auth.Service
}

func (r remediationDirectory) BindingStore(ctx context.Context, id string) (string, error) {
	return r.d.CatalogAdminBindingStore(ctx, id)
}
func (r remediationDirectory) DeviceActive(ctx context.Context, id string) (bool, error) {
	v, e := r.a.Get(ctx, id)
	return v.Active(), e
}
func (r remediationDirectory) DeviceName(context.Context, string) string { return "remediation" }

type remediationCloud struct {
	p             *pgxpool.Pool
	d             Devices
	dir           remediationDirectory
	s             *catalogadmin.Service
	sid, pid, did string
}

func remediationCloudSetup(t *testing.T) remediationCloud {
	t.Helper()
	p, a := openTestRepo(t)
	ctx := context.Background()
	d := NewDevices(p, 5*time.Second)
	v, e := a.Create(ctx, "remediation")
	if e != nil {
		t.Fatal(e)
	}
	sid, cid, pid, eid := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	did := v.Device.ID
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO stores(id,display_name,timezone) VALUES($1,'Review','Africa/Cairo')`, []any{sid}},
		{`INSERT INTO device_store_bindings(device_id,store_id) VALUES($1,$2)`, []any{did, sid}},
		{`INSERT INTO sync_events(event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id) VALUES($1,$2,'catalog.product.snapshot.v1',now(),'{}','\x00',$3)`, []any{eid, did, sid}},
		{`INSERT INTO catalog_categories(category_id,status,name_ar,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) VALUES($1,'active','Root',1,$2,$3,'\x00',now(),$4)`, []any{cid, eid, did, sid}},
		{`INSERT INTO catalog_products(product_id,name,top_category_id,is_active,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) VALUES($1,'Original',$2,true,7,$3,$4,'\x00',now(),$5)`, []any{pid, cid, eid, did, sid}},
		// Phase 17-R0: Product carries no SKU (ADR-0049) — the admin
		// search term lives on the product's VARIANT SKU (search matches
		// name or variant SKU).
		{`INSERT INTO catalog_product_variants(variant_id,product_id,sku,is_active,deleted,position,combination_key,variant_revision,catalog_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) VALUES($1,$2,'REVIEW-1',true,false,0,'default',1,1,$3,$4,'\x00',now(),$5)`, []any{uuid.NewString(), pid, eid, did, sid}},
	}
	for _, st := range stmts {
		if _, e = p.Exec(ctx, st.q, st.args...); e != nil {
			t.Fatal(e)
		}
	}
	dir := remediationDirectory{d, a}
	s := catalogadmin.NewService(d, dir)
	if _, e = s.ReportCapabilities(ctx, did, []string{catalogadmin.CapabilityV1}); e != nil {
		t.Fatal(e)
	}
	return remediationCloud{p, d, dir, s, sid, pid, did}
}
func (r remediationCloud) create(t *testing.T) catalogadmin.CommandView {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"product_id": r.pid, "arabic_name": "Changed"})
	v, e := r.s.Create(context.Background(), "remediation", r.sid, catalogadmin.TypeProductDetailsUpdateV1, r.pid, 7, b)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestRemediationReboundAck(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	v := r.create(t)
	if _, e := r.s.Poll(ctx, r.did, 10); e != nil {
		t.Fatal(e)
	}
	other := uuid.NewString()
	if _, e := r.p.Exec(ctx, `INSERT INTO stores(id,display_name,timezone) VALUES($1,'Other','Africa/Cairo')`, other); e != nil {
		t.Fatal(e)
	}
	if _, e := r.p.Exec(ctx, `UPDATE device_store_bindings SET store_id=$1 WHERE device_id=$2`, other, r.did); e != nil {
		t.Fatal(e)
	}
	if e := r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, catalogadmin.TargetApplied, catalogadmin.CodeApplied, r.pid, 7, 8); e == nil {
		t.Fatal("old Store ACK accepted")
	}
	got, e := r.s.Get(ctx, r.sid, v.ID)
	if e != nil || got.Targets[0].Status != catalogadmin.TargetSkippedRevoked || got.Aggregate == catalogadmin.AggregatePending {
		t.Fatal(got, e)
	}
}
func TestRemediationForgedOutcomes(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	v := r.create(t)
	for _, tc := range []struct {
		entity, code string
		pre, post    int64
	}{{uuid.NewString(), "APPLIED", 7, 8}, {r.pid, "ARBITRARY_RAW_ERROR_TEXT", 7, 8}, {r.pid, "APPLIED", 7, 0}, {r.pid, "APPLIED", 8, 9}, {r.pid, "APPLIED", 7, 7}, {r.pid, "APPLIED", 7, 1000}} {
		if e := r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, "APPLIED", tc.code, tc.entity, tc.pre, tc.post); e == nil {
			t.Fatal("forged ACK accepted", tc)
		}
	}
	got, e := r.s.Get(ctx, r.sid, v.ID)
	if e != nil || got.Converged || got.Targets[0].Status != "PENDING" {
		t.Fatal(got, e)
	}
	if e = r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, "APPLIED", "APPLIED", r.pid, 7, 8); e != nil {
		t.Fatal(e)
	}
	if e = r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, "APPLIED", "APPLIED", r.pid, 7, 8); e != nil {
		t.Fatal("identical retry", e)
	}
	if e = r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, "CONFLICT", catalogadmin.CodeConflict, r.pid, 7, 0); e == nil {
		t.Fatal("contradictory terminal accepted")
	}
}

type remediationCancelStore struct {
	catalogadmin.Store
	before func()
}

func (s remediationCancelStore) CancelCatalogAdminCommand(ctx context.Context, id string) (bool, error) {
	s.before()
	return s.Store.CancelCatalogAdminCommand(ctx, id)
}
func TestRemediationCancelAckRace(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	v := r.create(t)
	wrapper := remediationCancelStore{r.d, func() {
		if e := r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, "APPLIED", "APPLIED", r.pid, 7, 8); e != nil {
			t.Fatal(e)
		}
	}}
	s := catalogadmin.NewService(wrapper, r.dir)
	if _, e := s.Cancel(ctx, r.sid, v.ID); e == nil {
		t.Fatal("racing applied command cancelled")
	}
	got, e := r.s.Get(ctx, r.sid, v.ID)
	if e != nil || got.Aggregate != "APPLIED" || got.Targets[0].Status != "APPLIED" {
		t.Fatal(got, e)
	}
}
func TestRemediationPollCancelRace(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	v := r.create(t)
	var wg sync.WaitGroup
	wg.Add(2)
	var poll []catalogadmin.DueTarget
	var pollErr, cancelErr error
	go func() { defer wg.Done(); poll, pollErr = r.s.Poll(ctx, r.did, 10) }()
	go func() { defer wg.Done(); _, cancelErr = r.s.Cancel(ctx, r.sid, v.ID) }()
	wg.Wait()
	got, e := r.s.Get(ctx, r.sid, v.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(poll) > 0 && cancelErr == nil {
		t.Fatal("delivered and cancelled", pollErr, got)
	}
	if got.Status == "CANCELLED" && len(poll) > 0 {
		t.Fatal("cancelled command delivered")
	}
}
func TestRemediationAtomicTargetCreation(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	second, e := r.dir.a.Create(ctx, "second")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.p.Exec(ctx, `INSERT INTO device_store_bindings(device_id,store_id) VALUES($1,$2)`, second.Device.ID, r.sid); e != nil {
		t.Fatal(e)
	}
	if _, e = r.p.Exec(ctx, `CREATE FUNCTION target_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM catalog_admin_command_targets)>0 THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER target_failure BEFORE INSERT ON catalog_admin_command_targets FOR EACH ROW EXECUTE FUNCTION target_failure()`); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(map[string]any{"product_id": r.pid})
	if _, e = r.s.Create(ctx, "review", r.sid, catalogadmin.TypeProductDetailsUpdateV1, r.pid, 7, b); e == nil {
		t.Fatal("fault missed")
	}
	var commands, targets int
	r.p.QueryRow(ctx, `SELECT count(*) FROM catalog_admin_commands`).Scan(&commands)
	r.p.QueryRow(ctx, `SELECT count(*) FROM catalog_admin_command_targets`).Scan(&targets)
	if commands != 0 || targets != 0 {
		t.Fatal("partial create", commands, targets)
	}
}
func TestRemediationPayloadBindingAndUUIDv7(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	for _, payload := range []map[string]any{{"product_id": uuid.NewString()}, {"product_id": r.pid, "expected_catalog_revision": 8}} {
		b, _ := json.Marshal(payload)
		if _, e := r.s.Create(ctx, "review", r.sid, catalogadmin.TypeProductDetailsUpdateV1, r.pid, 7, b); e == nil {
			t.Fatal("contradictory payload accepted")
		}
	}
	v := r.create(t)
	if uuid.MustParse(v.ID).Version() != 7 || uuid.MustParse(v.Targets[0].ID).Version() != 7 {
		t.Fatal("UUID convention")
	}
	due, e := r.s.Poll(ctx, r.did, 10)
	if e != nil || len(due) != 1 {
		t.Fatal(due, e)
	}
	var payload map[string]any
	json.Unmarshal(due[0].Payload, &payload)
	if payload["product_id"] != r.pid || payload["expected_catalog_revision"] != float64(7) {
		t.Fatal(payload)
	}
}
func TestRemediationCatalogReads(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	r.create(t)
	for _, search := range []string{"", "REVIEW", "no match", "' OR TRUE --"} {
		rows, _, e := r.s.AdminProducts(ctx, r.sid, search, "", 20)
		if e != nil {
			t.Fatal(search, e)
		}
		if search == "" || search == "REVIEW" {
			if len(rows) != 1 || !rows[0].HasPending {
				t.Fatal(rows)
			}
		} else if len(rows) != 0 {
			t.Fatal("search scope", rows)
		}
	}
	detail, e := r.s.AdminProduct(ctx, r.sid, r.pid)
	if e != nil || !detail.HasPending {
		t.Fatal(detail, e)
	}
	other := uuid.NewString()
	_, e = r.s.AdminProduct(ctx, other, r.pid)
	var classified *apperr.Error
	if !errors.As(e, &classified) || classified.Kind != apperr.NotFound {
		t.Fatal("cross Store detail")
	}
	rows, _, e := r.s.AdminProducts(ctx, other, "", "", 20)
	if e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
}
func TestRemediationRevokedTargetTerminal(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	v := r.create(t)
	if e := r.dir.a.RevokeDevice(ctx, r.did); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.Poll(ctx, r.did, 10); e == nil {
		t.Fatal("revoked poll")
	}
	got, e := r.s.Get(ctx, r.sid, v.ID)
	if e != nil || got.Targets[0].Status != catalogadmin.TargetSkippedRevoked || got.Aggregate == "PENDING" {
		t.Fatal(got, e)
	}
	again, e := r.s.Get(ctx, r.sid, v.ID)
	if e != nil || again.Targets[0].Status != catalogadmin.TargetSkippedRevoked {
		t.Fatal(again, e)
	}
}
func TestRemediationPollByteBudget(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		b, _ := json.Marshal(map[string]any{"product_id": r.pid, "arabic_description": strings.Repeat("س", 2000), "english_description": strings.Repeat("e", 2000)})
		if _, e := r.s.Create(ctx, "review", r.sid, catalogadmin.TypeProductDetailsUpdateV1, r.pid, 7, b); e != nil {
			t.Fatal(e)
		}
	}
	seen := map[string]bool{}
	for len(seen) < 30 {
		due, e := r.s.Poll(ctx, r.did, 50)
		if e != nil || len(due) == 0 {
			t.Fatal(due, e)
		}
		if catalogadmin.CatalogPollBytes(due) > catalogadmin.MaxCatalogPollResponseBytes {
			t.Fatal("unbounded response")
		}
		for _, d := range due {
			seen[d.CommandID] = true
			if e = r.s.ReportOutcome(ctx, r.did, d.TargetID, "REJECTED", catalogadmin.CodeValidationFailed, r.pid, 0, 0); e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(seen) != 30 {
		t.Fatal(seen)
	}
}

func TestRemediationNoopConvergence(t *testing.T) {
	for _, wanted := range []bool{false, true} {
		t.Run(fmt.Sprint(wanted), func(t *testing.T) {
			r := remediationCloudSetup(t)
			ctx := context.Background()
			b, _ := json.Marshal(map[string]any{"product_id": r.pid, "sell_online": wanted})
			v, e := r.s.Create(ctx, "remediation", r.sid, catalogadmin.TypeProductOnlinePolicyUpdateV1, r.pid, 0, b)
			if e != nil {
				t.Fatal(e)
			}
			if e = r.s.ReportOutcome(ctx, r.did, v.Targets[0].ID, "APPLIED", "APPLIED", r.pid, 0, 0); e != nil {
				t.Fatal(e)
			}
			got, e := r.s.Get(ctx, r.sid, v.ID)
			if e != nil || got.Converged == wanted {
				t.Fatal(got, e)
			}
			list, _, e := r.s.List(ctx, r.sid, "", "", "", 10, "")
			if e != nil || len(list) != 1 || list[0].Converged != got.Converged {
				t.Fatal(list, e)
			}
		})
	}
}
