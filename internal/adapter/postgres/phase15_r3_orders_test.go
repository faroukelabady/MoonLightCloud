package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func TestR3ConfigurationOrderHistory(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	root, sub, tag := catalogProductFixture(t, env, 8100, "review-frame")
	p, c := "c001beef-0000-4000-8000-000000001111", "c001beef-0000-4000-8000-000000002222"
	event := "d001beef-0000-4000-8000-000000003333"
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-10-05T10:00:00Z", productPayload(p, "ML-REVIEW-001", "بردي", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, event)
	_, e := env.pool.Exec(ctx, `INSERT INTO catalog_product_configurations(configuration_id,product_id,kind,style_code,style_name_ar,style_name_en,color_code,color_name_ar,color_name_en,price_delta_egp_minor,price_delta_usd_minor,configuration_revision,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at) VALUES($1,$2,'frame','classic','قديم','Original','black','أسود','Black',30000,2500,1,1,$3,$4,'\x00',now())`, c, p, event, env.devID)
	if e != nil {
		t.Fatal(e)
	}
	store := catalogStore(env)
	if _, e = store.CreateProductMapping(ctx, "website", p, "500"); e != nil {
		t.Fatal(e)
	}
	if _, e = store.UpsertProductConfigurationMapping(ctx, "website", p, c, "500", "501"); e != nil {
		t.Fatal(e)
	}
	snapshot := orderTestSnapshot("website", "15001", orders.StatusPending, "pending", 130000)
	snapshot.Lines[0].VariationID = 501
	reconcileSnapshot(t, env, snapshot)
	get := func() (string, int64) {
		var label string
		var delta int64
		e := env.pool.QueryRow(ctx, "SELECT frame_style_name_en,configuration_price_delta_minor FROM commerce_online_order_lines WHERE external_order_id='15001'").Scan(&label, &delta)
		if e != nil {
			t.Fatal(e)
		}
		return label, delta
	}
	label, delta := get()
	if label != "Original" || delta != 30000 {
		t.Fatalf("initial %s %d", label, delta)
	}
	detail, e := store.GetOrderDetail(ctx, "website", "15001")
	if e != nil {
		t.Fatal(e)
	}
	if detail.Lines[0].ConfigurationID == nil || detail.Lines[0].FrameStyleNameEN == nil {
		t.Fatal("view fields unexpectedly absent")
	}
	t.Log("PASS F13 public detail now exposes stored frame snapshot")
	_, e = env.pool.Exec(ctx, "UPDATE catalog_product_configurations SET style_name_en='Renamed',price_delta_egp_minor=90000 WHERE configuration_id=$1", c)
	if e != nil {
		t.Fatal(e)
	}
	snapshot.Canonical = orders.StatusProcessing
	snapshot.ProviderStatus = "processing"
	reconcileSnapshot(t, env, snapshot)
	label, delta = get()
	if label != "Original" || delta != 30000 {
		t.Fatalf("history preservation failed %s %d", label, delta)
	}
	t.Log("PASS F11: public reconciliation preserves Original/30000 after current rename/reprice")
	usd := orderTestSnapshot("website", "15002", orders.StatusPending, "pending", 4500)
	usd.Currency = "USD"
	usd.Lines[0].VariationID = 501
	reconcileSnapshot(t, env, usd)
	var usdDelta int64
	e = env.pool.QueryRow(ctx, "SELECT configuration_price_delta_minor FROM commerce_online_order_lines WHERE external_order_id='15002'").Scan(&usdDelta)
	if e != nil {
		t.Fatal(e)
	}
	if usdDelta != 2500 {
		t.Fatal(usdDelta)
	}
	t.Log("PASS F12 USD order captures USD delta 2500")
	unknown := orderTestSnapshot("website", "15004", orders.StatusPending, "pending", 100000)
	unknown.Lines[0].VariationID = 599
	reconcileSnapshot(t, env, unknown)
	var unresolved, complete bool
	e = env.pool.QueryRow(ctx, "SELECT l.configuration_unresolved,o.mapping_complete FROM commerce_online_order_lines l JOIN commerce_online_orders o USING(provider_key,external_order_id) WHERE l.external_order_id='15004'").Scan(&unresolved, &complete)
	if e != nil {
		t.Fatal(e)
	}
	if !unresolved || complete {
		t.Fatal(unresolved, complete)
	}
	t.Log("PASS F13 initial mapped-base/unknown-selection order is incomplete")

	// Mapping arrives later, but persisted immutable line state remains unresolved.
	if _, e = env.pool.Exec(ctx, "UPDATE commerce_product_configuration_mappings SET external_configuration_id='599' WHERE configuration_id=$1", c); e != nil {
		t.Fatal(e)
	}
	unknown = orderTestSnapshot("website", "15004", orders.StatusProcessing, "processing", 100000)
	unknown.Lines[0].VariationID = 599
	reconcileSnapshot(t, env, unknown)
	e = env.pool.QueryRow(ctx, "SELECT l.configuration_unresolved,o.mapping_complete FROM commerce_online_order_lines l JOIN commerce_online_orders o USING(provider_key,external_order_id) WHERE l.external_order_id='15004'").Scan(&unresolved, &complete)
	if e != nil {
		t.Fatal(e)
	}
	if !unresolved || complete {
		t.Fatal("durable selection must determine completeness", unresolved, complete)
	}
	t.Log("F13 regression: late mapping preserves both unresolved history and incomplete header")

	if _, e = store.UpsertProductConfigurationMapping(ctx, "website", p, "00000000-0000-0000-0000-000000000000", "500", "502"); e != nil {
		t.Fatal(e)
	}
	nf := orderTestSnapshot("website", "15003", orders.StatusPending, "pending", 100000)
	nf.Lines[0].VariationID = 502
	reconcileSnapshot(t, env, nf)
	var nfID *string
	e = env.pool.QueryRow(ctx, "SELECT configuration_id::text FROM commerce_online_order_lines WHERE external_order_id='15003'").Scan(&nfID)
	if e != nil {
		t.Fatal(e)
	}
	if nfID != nil {
		t.Fatal(nfID)
	}
	t.Log("PASS F14 No Frame mapping normalizes to NULL business selection")
	// Each refresh allocates new input; a service-mutated slice cannot hide
	// persisted selection defects. Mapping loss never rewrites history.
	assertHeader := func(id string, want bool) {
		t.Helper()
		detail, e := store.GetOrderDetail(ctx, "website", id)
		if e != nil || detail.Summary.MappingComplete != want {
			t.Fatalf("header/detail %s: %v %+v", id, e, detail.Summary)
		}
		var actual bool
		if e = env.pool.QueryRow(ctx, "SELECT mapping_complete FROM commerce_online_orders WHERE external_order_id=$1", id).Scan(&actual); e != nil || actual != want {
			t.Fatal("durable verdict", e)
		}
	}
	// Unknown -> known stays historically unresolved; removal of that line
	// legitimately permits a fully complete remaining No Frame line.
	mixed := orderTestSnapshot("website", "15004", orders.StatusProcessing, "processing", 100000)
	mixed.Lines[0].VariationID = 599
	nfLine := mixed.Lines[0]
	nfLine.ExternalLineID = 2
	nfLine.VariationID = 502
	mixed.Lines = append(mixed.Lines, nfLine)
	reconcileSnapshot(t, env, mixed)
	assertHeader("15004", false)
	mixed = orderTestSnapshot("website", "15004", orders.StatusCompleted, "completed", 100000)
	mixed.Lines[0].ExternalLineID = 2
	mixed.Lines[0].VariationID = 502
	reconcileSnapshot(t, env, mixed)
	assertHeader("15004", true)
	// Base mapping disappearance affects operational completeness, while the
	// original captured label/delta remain frozen.
	command, e := env.pool.Exec(ctx, "DELETE FROM commerce_product_mappings WHERE provider_key='website' AND product_id=$1", p)
	if e != nil || command.RowsAffected() != 1 {
		t.Fatal("mapping fixture", e)
	}
	loss := orderTestSnapshot("website", "15001", orders.StatusCompleted, "completed", 140000)
	loss.Lines[0].VariationID = 501
	reconcileSnapshot(t, env, loss)
	assertHeader("15001", false)
	label, delta = get()
	if label != "Original" || delta != 30000 {
		t.Fatal("mapping loss changed history")
	}
	if _, e = store.CreateProductMapping(ctx, "website", p, "500"); e != nil {
		t.Fatal(e)
	}
	// Disabled and missing-current-currency metadata never revise a captured
	// historical selection. It is not recaptured from current catalog.
	command, e = env.pool.Exec(ctx, "UPDATE catalog_product_configurations SET enabled=false,price_delta_usd_minor=NULL WHERE configuration_id=$1", c)
	if e != nil || command.RowsAffected() != 1 {
		t.Fatal("disable fixture", e)
	}
	refreshUSD := orderTestSnapshot("website", "15002", orders.StatusCompleted, "completed", 5000)
	refreshUSD.Currency = "USD"
	refreshUSD.Lines[0].VariationID = 501
	reconcileSnapshot(t, env, refreshUSD)
	if e = env.pool.QueryRow(ctx, "SELECT configuration_price_delta_minor FROM commerce_online_order_lines WHERE external_order_id='15002'").Scan(&usdDelta); e != nil || usdDelta != 2500 {
		t.Fatal("currency gap overwrote history", e)
	}
	// Fail AFTER header/lines/verdict writes, before commit. All state rolls back.
	if _, e = env.pool.Exec(ctx, `CREATE FUNCTION r3_order_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER r3_order_fault BEFORE INSERT ON commerce_online_order_addresses FOR EACH ROW EXECUTE FUNCTION r3_order_fault()`); e != nil {
		t.Fatal(e)
	}
	broken := orderTestSnapshot("website", "15004", orders.StatusPending, "pending", 100000)
	broken.Lines[0].VariationID = 599
	gen, e := store.BeginOrderReconcile(ctx, "website", "15004")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.ReconcileProjectedOrder(ctx, broken, orders.Fingerprint(broken), gen); e == nil {
		t.Fatal("fault did not fire")
	}
	assertHeader("15004", true)
	if _, e = env.pool.Exec(ctx, `DROP TRIGGER r3_order_fault ON commerce_online_order_addresses; DROP FUNCTION r3_order_fault()`); e != nil {
		t.Fatal(e)
	}
	reopened, e := pgxpool.New(ctx, env.pool.Config().ConnConfig.ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	reopenedStore := NewDevices(reopened, 5*time.Second)
	persisted, e := reopenedStore.GetOrderDetail(ctx, "website", "15004")
	if e != nil || !persisted.Summary.MappingComplete || len(persisted.Lines) != 1 {
		t.Fatal("restart changed reconciled verdict", e)
	}
	// Exact pre-configuration-migration shape: a variation identity existed
	// but no selection was captured. A later lookup must not make this old
	// row look like a deliberately captured No Frame selection.
	legacy := orderTestSnapshot("website", "15005", orders.StatusPending, "pending", 100000)
	legacy.Lines[0].VariationID = 599
	reconcileSnapshot(t, env, legacy)
	command, e = env.pool.Exec(ctx, `UPDATE commerce_online_order_lines SET configuration_id=NULL,provider_configuration_id=NULL,configuration_unresolved=false,frame_style_code=NULL,frame_style_name_ar=NULL,frame_style_name_en=NULL,frame_color_code=NULL,frame_color_name_ar=NULL,frame_color_name_en=NULL,configuration_price_delta_minor=NULL WHERE external_order_id='15005'`)
	if e != nil || command.RowsAffected() != 1 {
		t.Fatal("legacy fixture", e)
	}
	legacy = orderTestSnapshot("website", "15005", orders.StatusProcessing, "processing", 100000)
	legacy.Lines[0].VariationID = 599
	reconcileSnapshot(t, env, legacy)
	assertHeader("15005", false)
	t.Log("fresh inputs, mixed/add/remove lines, mapping loss, disabled/currency-gap history, post-verdict fault rollback and reopened-pool views verified")

}
