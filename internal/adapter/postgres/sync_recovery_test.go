package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/syncrecovery"
)

// TestSyncRecoveryLedgerStoreScopeAndOrdering is the Phase 20 R6 real
// PostgreSQL proof for the recovery protocol: byte-order keyset pages
// over mixed UUIDv4/UUIDv7 identities, the server-owned missing ledger,
// ledger reset on re-marking, and Store-scoped counts that exclude
// revoked devices and never see another Store.
func TestSyncRecoveryLedgerStoreScopeAndOrdering(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	devices := NewDevices(pool, 5*time.Second)
	svc := syncrecovery.NewService(devices)

	newDevice := func(name string) string {
		p, err := authSvc.Create(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return p.Device.ID
	}
	newStore := func() string {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO stores (id, display_name, timezone) VALUES ($1, 'R6', 'Africa/Cairo')`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	bind := func(device, store string) {
		if _, err := pool.Exec(ctx, `INSERT INTO device_store_bindings (device_id, store_id) VALUES ($1, $2)`, device, store); err != nil {
			t.Fatal(err)
		}
	}
	event := func(device, store, id string) {
		if _, err := pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash, store_id)
			VALUES ($1, $2, 'sale.finalized.v3', now(), '{}', decode('aa', 'hex'), $3)`, id, device, store); err != nil {
			t.Fatal(err)
		}
	}

	s1, s2 := newStore(), newStore()
	a, b, c := newDevice("r6-a"), newDevice("r6-b"), newDevice("r6-c")
	bind(a, s1)
	bind(b, s1)
	bind(c, s2)
	// Mixed v4/v7 identities: the order Retail walks is byte order.
	v7a, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{uuid.NewString(), v7a.String(), uuid.NewString(), uuid.NewString()}
	sort.Strings(ids)
	for _, id := range ids {
		event(a, s1, id)
	}
	cEvent := uuid.NewString()
	event(c, s2, cEvent)
	if _, err := pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status) VALUES ($1, 'sale_projection.v2', 'retry')`, cEvent); err != nil {
		t.Fatal(err)
	}

	if err := svc.MarkRestoreRequired(ctx, []string{a, b, c}, "restored"); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Status(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Required || report.DevicesUnresolved != 2 || report.ProjectionsPending != 0 {
		t.Fatalf("store 1 must see only its own devices and projections: %+v", report)
	}
	other, err := svc.Status(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if other.DevicesUnresolved != 1 || other.ProjectionsPending != 1 || other.StoreReconciled {
		t.Fatalf("store 2 sees only its own device and pending projection: %+v", other)
	}

	identities := func(list []string) []syncrecovery.EventIdentity {
		out := make([]syncrecovery.EventIdentity, 0, len(list))
		for _, id := range list {
			out = append(out, syncrecovery.EventIdentity{EventID: id, PayloadHash: "aa"})
		}
		return out
	}
	if _, err := svc.Verify(ctx, a, syncrecovery.VerifyRequest{Events: identities(ids[:2]), DeclaredTotal: int64(len(ids))}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(ctx, a, syncrecovery.VerifyRequest{Events: identities(ids[:2])}); err == nil {
		t.Fatal("a replayed page must not advance the byte-order cursor")
	}
	resp, err := svc.Verify(ctx, a, syncrecovery.VerifyRequest{Events: identities(ids[2:]), EndOfHistory: true})
	if err != nil || len(resp.Missing) != 0 {
		t.Fatalf("second page: %+v %v", resp, err)
	}
	if err := svc.Complete(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE devices SET status = 'revoked', revoked_at = now() WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	report, err = svc.Status(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if report.Required || report.DevicesUnresolved != 0 || !report.StoreReconciled {
		t.Fatalf("store 1 reconciled once its active devices are resolved (store 2 must not block it): %+v", report)
	}

	// Re-marking resets the ledger: no completion without a new walk.
	if err := svc.MarkRestoreRequired(ctx, []string{a}, "restored again"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(ctx, a, 0); err == nil {
		t.Fatal("a re-marked device must walk its history again")
	}
	// The server-owned missing ledger: an absent identity blocks completion
	// even when the client claims last_missing=0.
	absent := "ffffffff-ffff-4fff-bfff-ffffffffffff"
	walk := append(append([]string{}, ids...), absent)
	if _, err := svc.Verify(ctx, a, syncrecovery.VerifyRequest{Events: identities(walk), EndOfHistory: true, DeclaredTotal: int64(len(walk))}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(ctx, a, 0); err == nil {
		t.Fatal("a pass with a server-recorded missing identity must not complete")
	}
	progress, _, err := svc.VerifyProgress(ctx, a)
	if err != nil || progress.Missing != 1 {
		t.Fatalf("missing ledger: %+v %v", progress, err)
	}
}
