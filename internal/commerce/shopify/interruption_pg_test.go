package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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
	source := &r1MutableSource{desired: fixedDesired("prod-1", "PAP-001")}
	sa := commerce.NewCommerceService(newRegistryWith(t, pa), r1CoordinatedMappings{maps, coordA}, source, nil)
	sb := commerce.NewCommerceService(newRegistryWith(t, pb), r1CoordinatedMappings{maps, coordB}, source, nil)
	initial, e := sa.SyncProduct(ctx, "shopify-main", "prod-1")
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
	go func() { _, e := sa.SyncProduct(oldCtx, "shopify-main", "prod-1"); done <- e }()
	<-received
	oldCancel()
	if e := <-done; e == nil {
		t.Fatal("cancelled call succeeded")
	}
	fresh := fixedDesired("prod-1", "PAP-001")
	fresh.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "NEW-TITLE"}}
	fresh.CatalogRevision, fresh.PolicyRevision, fresh.InventoryRevision = 9, 9, 9
	source.set(fresh)
	if _, e := sb.SyncProduct(ctx, "shopify-main", "prod-1"); e != nil {
		t.Fatal(e)
	}
	if got := h.remoteState(initial.ExternalProductID).title; got != "NEW-TITLE" {
		t.Fatal("new operation did not complete", got)
	}
	releaseOld()
	<-applied
	if got := h.remoteState(initial.ExternalProductID).title; got != "NEW-TITLE" {
		t.Fatalf("F2 OPEN: cancelled already-received old mutation applied after newer completion: title=%q", got)
	}
}
