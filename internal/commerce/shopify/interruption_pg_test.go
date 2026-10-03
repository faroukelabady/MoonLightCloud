package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Independent interruption probe: a request already received by the remote
// server can apply after the caller is cancelled and its DB lock released.
func TestR1InFlightCancellation(t *testing.T) {
	database := testutil.Isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, e := pgxpool.New(ctx, database)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := pgxpool.New(ctx, database)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	coordA, coordB := postgres.NewDevices(a, 5*time.Second), postgres.NewDevices(b, 5*time.Second)
	h := newHarness(t)
	pa, _ := NewShopifyProvider(testConfig(), harnessClient(t, h), coordA)
	pb, _ := NewShopifyProvider(testConfig(), harnessClient(t, h), coordB)
	maps := newStubMappings()
	source := &r1MutableSource{desired: fixedDesired("019c0000-0000-7000-8000-000000000011", "PAP-001")}
	sa := commerce.NewCommerceService(newRegistryWith(t, pa), r1CoordinatedMappings{maps, coordA}, source, nil)
	sb := commerce.NewCommerceService(newRegistryWith(t, pb), r1CoordinatedMappings{maps, coordB}, source, nil)
	initial, e := sa.SyncProduct(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011")
	if e != nil {
		t.Fatal(e)
	}
	old := source.desired
	old.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "OLD-TITLE"}}
	source.set(old)
	received, release, applied := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var fired atomic.Bool
	var released sync.Once
	releaseOld := func() { released.Do(func() { close(release) }) }
	defer releaseOld()
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var request gqlRequest
		_ = json.Unmarshal(raw, &request)
		if operationName(request.Query) == "MoonlightProductUpdate" && fired.CompareAndSwap(false, true) {
			close(received)
			<-release
			h.serve(w, r)
			close(applied)
			return
		}
		h.serve(w, r)
	})
	oldCtx, oldCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, e := sa.SyncProduct(oldCtx, "shopify-main", "019c0000-0000-7000-8000-000000000011")
		done <- e
	}()
	<-received
	oldCancel()
	if e := <-done; e == nil {
		t.Fatal("cancelled call succeeded")
	}
	fresh := fixedDesired("019c0000-0000-7000-8000-000000000011", "PAP-001")
	fresh.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "NEW-TITLE"}}
	fresh.CatalogRevision, fresh.PolicyRevision, fresh.InventoryRevision = 9, 9, 9
	source.set(fresh)
	// Every intended delivery is protected by committed evidence visible to
	// an independent connection before the old response is available.
	blocked, err := coordB.GetProductMutationBarrier(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011")
	if err != nil || blocked.OperationID == "" || blocked.State != "uncertain" {
		t.Fatalf("missing durable uncertainty: %+v %v", blocked, err)
	}
	before := len(h.recorded())
	if _, err := sb.SyncProduct(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011"); err == nil || !strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
		t.Fatalf("newer work bypassed uncertainty: %v", err)
	}
	if len(h.recorded()) != before {
		t.Fatal("blocked newer work reached provider")
	}
	if err := coordB.ResolveProductMutationBarrier(ctx, "shopify-main", blocked.ProductID, blocked.OperationID, "remote_completed", false); err == nil {
		t.Fatal("unconfirmed resolution accepted")
	}
	tag, err := b.Exec(ctx, "UPDATE commerce_product_mutation_barriers SET created_at=now()-interval '30 days' WHERE operation_id=$1", blocked.OperationID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatal("age fixture", err)
	}
	// A new coordinator represents process restart. Neither elapsed time nor
	// matching remote state releases the old barrier.
	restarted := postgres.NewDevices(b, 5*time.Second)
	if err := restarted.WithProductSync(ctx, "shopify-main", blocked.ProductID, func(context.Context) error { t.Fatal("restart callback ran"); return nil }); err == nil {
		t.Fatal("restart erased uncertainty")
	}
	releaseOld()
	<-applied
	if got := h.remoteState(initial.ExternalProductID).title; got != "OLD-TITLE" {
		t.Fatal("old request did not actually apply", got)
	}
	if _, err := sb.SyncProduct(ctx, "shopify-main", blocked.ProductID); err == nil {
		t.Fatal("current remote snapshot automatically resolved uncertainty")
	}
	// The fixture's applied signal is definitive provider-side completion
	// evidence, not merely a current Product read. Operator confirmation now
	// permits an explicit fresh canonical sync; no automatic resend occurs.
	if err := restarted.ResolveProductMutationBarrier(ctx, "shopify-main", blocked.ProductID, blocked.OperationID, "remote_completed", true); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.SyncProduct(ctx, "shopify-main", blocked.ProductID); err != nil {
		t.Fatal(err)
	}
	if got := h.remoteState(initial.ExternalProductID).title; got != "NEW-TITLE" {
		t.Fatal("fresh state did not converge", got)
	}
	var count int
	if err := b.QueryRow(ctx, "SELECT count(*) FROM commerce_product_mutation_barriers WHERE operation_id=$1 AND state='resolved' AND resolution='remote_completed'", blocked.OperationID).Scan(&count); err != nil || count != 1 {
		t.Fatal("resolution audit missing", err)
	}
}
