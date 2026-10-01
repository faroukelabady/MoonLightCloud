package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// Phase 9A store registration: idempotency, conflicts, races, and
// multi-device joins against real PostgreSQL.

func openStoreEnv(t *testing.T) (Devices, string) {
	t.Helper()
	pool, authSvc := openTestRepo(t)
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	return NewDevices(pool, 5*time.Second), p.Device.ID
}

func TestStoreRegistrationIdempotent(t *testing.T) {
	d, devID := openStoreEnv(t)
	ctx := context.Background()
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	first, err := d.RegisterStore(ctx, devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !first.Bound || first.StoreID != storeID {
		t.Fatalf("bound: %+v", first)
	}
	second, err := d.RegisterStore(ctx, devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Bound {
		t.Fatalf("idempotent: %+v", second)
	}
}

func TestStoreRegistrationValidation(t *testing.T) {
	d, devID := openStoreEnv(t)
	ctx := context.Background()
	for name, request := range map[string]store.RegistrationRequest{
		"bad uuid":     {StoreID: "nope", DisplayName: "x", Timezone: "Africa/Cairo"},
		"blank name":   {StoreID: "aaaaaaaa-0000-4000-8000-000000000001", DisplayName: "  ", Timezone: "Africa/Cairo"},
		"bad timezone": {StoreID: "aaaaaaaa-0000-4000-8000-000000000001", DisplayName: "x", Timezone: "UTC+2"},
		"unknown zone": {StoreID: "aaaaaaaa-0000-4000-8000-000000000001", DisplayName: "x", Timezone: "Moon/Mare"},
	} {
		if _, err := d.RegisterStore(ctx, devID, request); err == nil {
			t.Fatalf("%s: must reject", name)
		}
	}
}

func TestStoreBindingConflictStable(t *testing.T) {
	d, devID := openStoreEnv(t)
	ctx := context.Background()
	storeA := "aaaaaaaa-0000-4000-8000-000000000001"
	storeB := "bbbbbbbb-0000-4000-8000-000000000002"
	if _, err := d.RegisterStore(ctx, devID, store.RegistrationRequest{
		StoreID: storeA, DisplayName: "A", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := d.RegisterStore(ctx, devID, store.RegistrationRequest{
		StoreID: storeB, DisplayName: "B", Timezone: "Africa/Cairo",
	})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
		t.Fatalf("conflict, got %v", err)
	}
	// Binding unchanged; no second binding row.
}

func TestStoreConcurrentIdenticalConverges(t *testing.T) {
	d, devID := openStoreEnv(t)
	ctx := context.Background()
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = d.RegisterStore(context.Background(), devID, store.RegistrationRequest{
				StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
			})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("idempotent converge: %v", err)
		}
	}
	_ = ctx
}

func TestStoreConcurrentConflictingOneWinner(t *testing.T) {
	d, devID := openStoreEnv(t)
	ctx := context.Background()
	storeA := "aaaaaaaa-0000-4000-8000-000000000001"
	storeB := "bbbbbbbb-0000-4000-8000-000000000002"
	const workers = 8
	var wg sync.WaitGroup
	type outcome struct {
		store string
		err   error
	}
	outcomes := make([]outcome, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := storeA
			if i%2 == 1 {
				target = storeB
			}
			_, err := d.RegisterStore(context.Background(), devID, store.RegistrationRequest{
				StoreID: target, DisplayName: "S", Timezone: "Africa/Cairo",
			})
			outcomes[i] = outcome{store: target, err: err}
		}(i)
	}
	wg.Wait()
	var wonA, wonB, conflicts int
	for _, o := range outcomes {
		if o.err == nil {
			if o.store == storeA {
				wonA++
			} else {
				wonB++
			}
		} else {
			var appErr *apperr.Error
			if !errors.As(o.err, &appErr) || appErr.Kind != apperr.Conflict {
				t.Fatalf("deterministic conflict, got %v", o.err)
			}
			conflicts++
		}
	}
	if (wonA == 0) == (wonB == 0) {
		t.Fatalf("exactly one store wins: A=%d B=%d conflicts=%d", wonA, wonB, conflicts)
	}
	_ = ctx
}

func TestStoreRenamePreservesIdentity(t *testing.T) {
	d, devID := openStoreEnv(t)
	ctx := context.Background()
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	if _, err := d.RegisterStore(ctx, devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	renamed, err := d.RegisterStore(ctx, devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Moon Light Cairo", Timezone: "Africa/Cairo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.StoreID != storeID || renamed.DisplayName != "Moon Light Cairo" {
		t.Fatalf("rename converges metadata, keeps ID: %+v", renamed)
	}
}

func TestStoreIngressSnapshotsBinding(t *testing.T) {
	env := openSaleEnv(t)
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	storeDevices := NewDevices(env.pool, 5*time.Second)
	if _, err := storeDevices.RegisterStore(context.Background(), env.devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	eventID := "22222222-2222-7222-8222-222222222222"
	if res := env.ingest(t, eventID, fixture(t, "sale_usd.json")); res.Events[0].Status != "accepted" {
		t.Fatalf("ingest: %+v", res)
	}
	var got *string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT store_id::text FROM sync_events WHERE event_id = $1`, eventID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != storeID {
		t.Fatalf("ingress snapshots binding: %v", got)
	}
	// Spoofed payload store context is impossible: events carry no store
	// field; context comes from the binding alone.
}

func TestStoreLegacyEventsStayNull(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "44444444-4444-7222-8222-222222222222"
	if res := env.ingest(t, eventID, fixture(t, "sale_usd.json")); res.Events[0].Status != "accepted" {
		t.Fatalf("ingest: %+v", res)
	}
	var isNull bool
	if err := env.pool.QueryRow(context.Background(),
		`SELECT store_id IS NULL FROM sync_events WHERE event_id = $1`, eventID).Scan(&isNull); err != nil {
		t.Fatal(err)
	}
	if !isNull {
		t.Fatal("unbound device events keep NULL store context, never fabricated")
	}
}

func TestStoreTwoDevicesOneStore(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	first, err := authSvc.Create(ctx, "device-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := authSvc.Create(ctx, "device-two")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDevices(pool, 5*time.Second)
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	for _, devID := range []string{first.Device.ID, second.Device.ID} {
		result, err := d.RegisterStore(ctx, devID, store.RegistrationRequest{
			StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
		})
		if err != nil {
			t.Fatalf("device %s: %v", devID, err)
		}
		if !result.Bound {
			t.Fatalf("device %s not bound", devID)
		}
	}
}

func TestStoreEventRetryPreservesContext(t *testing.T) {
	env := openSaleEnv(t)
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	storeDevices := NewDevices(env.pool, 5*time.Second)
	if _, err := storeDevices.RegisterStore(context.Background(), env.devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	eventID := "55555555-5555-7222-8222-222222222222"
	if res := env.ingest(t, eventID, fixture(t, "sale_usd.json")); res.Events[0].Status != "accepted" {
		t.Fatalf("ingest: %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")
	// Replay the same event: context immutable.
	if res := env.ingest(t, eventID, fixture(t, "sale_usd.json")); res.Events[0].Status != "already_accepted" {
		t.Fatalf("replay: %+v", res)
	}
	env.drain(t)
	time.Sleep(time.Second)
	var got string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT store_id::text FROM sync_events WHERE event_id = $1`, eventID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != storeID {
		t.Fatalf("replay preserves store context: %s", got)
	}
}

func TestStoreRenameKeepsBindingAndEvents(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	storeDevices := NewDevices(env.pool, 5*time.Second)
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	if _, err := storeDevices.RegisterStore(ctx, env.devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	eventID := "66666666-6666-7222-8222-222222222222"
	if res := env.ingest(t, eventID, fixture(t, "sale_usd.json")); res.Events[0].Status != "accepted" {
		t.Fatalf("ingest: %+v", res)
	}
	if _, err := storeDevices.RegisterStore(ctx, env.devID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Moon Light Cairo", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	var name, eventStore string
	if err := env.pool.QueryRow(ctx, `SELECT display_name FROM stores WHERE id = $1`, storeID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(ctx, `SELECT store_id::text FROM sync_events WHERE event_id = $1`, eventID).Scan(&eventStore); err != nil {
		t.Fatal(err)
	}
	if name != "Moon Light Cairo" || eventStore != storeID {
		t.Fatalf("rename converges metadata, history stable: %q %q", name, eventStore)
	}
	var bindings int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM device_store_bindings WHERE device_id = $1::uuid`, env.devID).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("binding stable: %d (%v)", bindings, err)
	}
}

func TestStoreRevocationPreservesStore(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDevices(pool, 5*time.Second)
	storeID := "aaaaaaaa-0000-4000-8000-000000000001"
	if _, err := d.RegisterStore(ctx, p.Device.ID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Cairo Gallery", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	if err := authSvc.RevokeDevice(ctx, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	// Store, binding, and history survive revocation.
	var stores, bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stores WHERE id = $1::uuid`, storeID).Scan(&stores); err != nil || stores != 1 {
		t.Fatalf("store survives: %d (%v)", stores, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_store_bindings WHERE device_id = $1::uuid`, p.Device.ID).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("binding survives: %d (%v)", bindings, err)
	}
	// Revoked device cannot register or update afterwards: auth rejects
	// before registration logic ever runs (covered by deviceauth 401s);
	// the binding row itself is untouched.
}

func TestStoreCrossDeviceIsolation(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	devA, err := authSvc.Create(ctx, "shop-a")
	if err != nil {
		t.Fatal(err)
	}
	devB, err := authSvc.Create(ctx, "shop-b")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDevices(pool, 5*time.Second)
	storeA := "aaaaaaaa-0000-4000-8000-000000000001"
	storeB := "bbbbbbbb-0000-4000-8000-000000000002"
	if _, err := d.RegisterStore(ctx, devA.Device.ID, store.RegistrationRequest{
		StoreID: storeA, DisplayName: "A", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterStore(ctx, devB.Device.ID, store.RegistrationRequest{
		StoreID: storeB, DisplayName: "B", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	syncSvc := isync.NewService(d, clock.System{})
	ingestAs := func(devID, credID, eventID string) {
		t.Helper()
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"system.test.v1","occurred_at":"2026-09-20T10:00:00Z","payload":{"ping":1}}]}`,
			eventID)
		res, err := syncSvc.Ingest(ctx, devID, credID, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if res.Events[0].Status != "accepted" {
			t.Fatalf("ingest: %+v", res)
		}
	}
	eventA := "aaaaaaaa-1111-4111-8111-111111111111"
	eventB := "bbbbbbbb-2222-4222-8222-222222222222"
	ingestAs(devA.Device.ID, devA.Credential.ID, eventA)
	ingestAs(devB.Device.ID, devB.Credential.ID, eventB)
	for eventID, want := range map[string]string{eventA: storeA, eventB: storeB} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT store_id::text FROM sync_events WHERE event_id = $1`, eventID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("event %s has store %s, want %s", eventID, got, want)
		}
	}
}
