package shopify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Phase 11-R2: GraphQL outcome classification and durable mutation
// settlement are separate decisions. After a mutation is dispatched, its
// barrier is retained unless the ENTIRE response carries affirmative,
// validated evidence of a definitive outcome for THAT mutation. These
// tests run against real PostgreSQL and the local TLS harness and assert
// actual database rows and provider-call counts.

const r2Product = "019c0000-0000-7000-8000-000000000011"

type r2Fixture struct {
	t        *testing.T
	ctx      context.Context
	database string
	pool     *pgxpool.Pool
	store    postgres.Devices
	h        *shopifyHarness
	provider *ShopifyProvider
	product  string
	external string
}

// newPool opens an independent session against the same isolated
// database: independent coordinators must not share a connection.
func (f *r2Fixture) newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(f.ctx, f.database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newR2Fixture(t *testing.T) *r2Fixture {
	t.Helper()
	database := testutil.Isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewDevices(pool, 5*time.Second)
	h := newHarness(t)
	provider, err := NewShopifyProvider(testConfig(), harnessClient(t, h), store)
	if err != nil {
		t.Fatal(err)
	}
	created, err := provider.UpsertProduct(ctx, upsertRequest(r2Product, "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	return &r2Fixture{
		t: t, ctx: ctx, database: database, pool: pool, store: store, h: h, provider: provider,
		product: r2Product, external: created.ExternalProductID,
	}
}

// mutationRequest is the update request under test (same desired-state
// shape as the review probes).
func (f *r2Fixture) mutationRequest() commerce.ProductUpsertRequest {
	req := staleRequest(f.external)
	req.ProductID = f.product
	return req
}

// barrierActive reports whether an active barrier exists (durably).
func (f *r2Fixture) barrierActive() (string, bool) {
	status, err := f.store.GetProductMutationBarrier(f.ctx, "shopify-main", f.product)
	if err != nil {
		f.t.Fatalf("barrier status: %v", err)
	}
	return status.OperationID, status.OperationID != ""
}

// interceptMutation routes one mutation operation to respond while
// asserting the barrier is committed and visible before the provider
// receives the request (§5.3).
func (f *r2Fixture) interceptMutation(t *testing.T, operation string, apply bool, status int, body string) {
	t.Helper()
	f.h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var request gqlRequest
		_ = json.Unmarshal(raw, &request)
		if operationName(request.Query) != operation {
			f.h.serve(w, r)
			return
		}
		var fingerprint, state string
		if err := f.pool.QueryRow(f.ctx,
			"SELECT request_fingerprint,state FROM commerce_product_mutation_barriers WHERE provider_key='shopify-main' AND product_id=$1 AND state IN ('in_flight','uncertain')",
			f.product).Scan(&fingerprint, &state); err != nil {
			t.Error("no committed barrier visible before provider send:", err)
		}
		if fingerprint != fmt.Sprintf("%x", sha256.Sum256(raw)) || state != "in_flight" {
			t.Error("wrong durable request evidence:", fingerprint, state)
		}
		if apply {
			f.h.serve(httptest.NewRecorder(), r) // the remote applies the mutation
		}
		w.Header().Set("X-Shopify-API-Version", "2026-10")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

// restoreHarness returns dispatch to the normal fake Shopify.
func (f *r2Fixture) restoreHarness() {
	f.h.server.Config.Handler = http.HandlerFunc(f.h.serve)
}

// §5 mandatory reproduction matrix: response → barrier expectation.
func TestR2GraphQLOutcomeBarrierMatrix(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int  // HTTP status (0 means 200)
		apply  bool // the remote applied before answering (worst case)
		retain bool // barrier must remain active
	}{
		{"http200_internal_server_error",
			`{"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, 0, true, true},
		{"http200_unknown_code",
			`{"errors":[{"message":"uncertain execution","extensions":{"code":"UNRECOGNIZED_FAILURE"}}]}`, 0, true, true},
		{"partial_data_plus_internal_error",
			`{"data":{"productUpdate":null},"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, 0, true, true},
		{"missing_error_code",
			`{"errors":[{"message":"boom"}]}`, 0, true, true},
		{"throttled_first_internal_second",
			`{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}},{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, 0, true, true},
		{"internal_first_throttled_second",
			`{"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}},{"message":"Throttled","extensions":{"code":"THROTTLED"}}]}`, 0, true, true},
		{"malformed_incomplete_error_evidence",
			`{"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}},{"extensions":{"code":"THROTTLED"}}]}`, 0, true, true},
		// Non-2xx failures that do not establish refusal keep the barrier
		// (response loss / server failure are not settlement evidence).
		{"http500_html_error", `internal error`, 500, true, true},
		{"http503_unavailable", `{"errors":[{"message":"unavailable"}]}`, 503, true, true},
		// Documented definitive pre-execution refusals: the whole response
		// establishes the mutation never executed.
		{"definitive_pre_execution_throttling",
			`{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}}]}`, 0, false, false},
		{"definitive_authorization_refusal",
			`{"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"}}]}`, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newR2Fixture(t)
			status := tc.status
			if status == 0 {
				status = 200
			}
			f.interceptMutation(t, "MoonlightProductUpdate", tc.apply, status, tc.body)
			req := f.mutationRequest()
			_, err := f.provider.UpsertProduct(f.ctx, req)
			if err == nil {
				t.Fatal("error response ignored")
			}
			if len(err.Error()) > 512 {
				t.Fatalf("unbounded error: %q", err.Error())
			}
			operation, active := f.barrierActive()
			if active != tc.retain {
				t.Fatalf("barrier retained=%v want %v (operation %q)", active, tc.retain, operation)
			}
			before := len(f.h.recorded())
			f.restoreHarness()
			next, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), f.store)
			if err != nil {
				t.Fatal(err)
			}
			if tc.retain {
				_, err = next.UpsertProduct(f.ctx, req)
				if err == nil || !strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
					t.Fatalf("uncertain write must stay blocked, got %v", err)
				}
				if len(f.h.recorded()) != before {
					t.Errorf("blocked retry reached the provider: before=%d after=%d", before, len(f.h.recorded()))
				}
			} else {
				if _, err := next.UpsertProduct(f.ctx, req); err != nil {
					t.Fatalf("released barrier must admit the next operation: %v", err)
				}
				if len(f.h.recorded()) == before {
					t.Error("released barrier admitted no provider work")
				}
			}
		})
	}
}

// §5: a valid successful mutation releases exactly its own operation.
func TestR2SuccessfulMutationReleasesExactOperation(t *testing.T) {
	f := newR2Fixture(t)
	req := f.mutationRequest()
	if _, err := f.provider.UpsertProduct(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, active := f.barrierActive(); active {
		t.Fatal("successful mutation left a barrier behind")
	}
	// The same product syncs again normally.
	if _, err := f.provider.UpsertProduct(f.ctx, req); err != nil {
		t.Fatal(err)
	}
}

// §6 delayed-side-effect proof: an older mutation with uncertain
// settlement keeps the barrier durable; a newer synchronization is
// blocked with zero provider admissions; the simulated remote effect
// applies later; only explicit confirmed settlement of the exact
// operation unblocks a fresh synchronization that rereads canonical
// desired state. Deterministic channel ordering (no sleep).
func TestR2DelayedRemoteEffectRequiresConfirmedSettlement(t *testing.T) {
	f := newR2Fixture(t)
	received := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseOld := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseOld()

	// The provider receives the older mutation, then the remote settles
	// it LATER (delayed side effect) while answering with an internal
	// execution error: settlement stays uncertain.
	f.h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var request gqlRequest
		_ = json.Unmarshal(raw, &request)
		if operationName(request.Query) != "MoonlightProductUpdate" {
			f.h.serve(w, r)
			return
		}
		close(received)
		<-release
		// Delayed simulated remote effect of the OLD mutation.
		f.h.serve(httptest.NewRecorder(), r)
		w.Header().Set("X-Shopify-API-Version", "2026-10")
		w.WriteHeader(200)
		io.WriteString(w, `{"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`)
	})

	old := f.mutationRequest()
	oldDone := make(chan error, 1)
	go func() {
		_, err := f.provider.UpsertProduct(f.ctx, old)
		oldDone <- err
	}()
	<-received // the older mutation is with the provider right now

	// A newer synchronization is attempted through an independent
	// instance while the older mutation is still in flight.
	maps := newStubMappings()
	source := &r1MutableSource{desired: fixedDesired(f.product, "PAP-001")}
	fresh := source.desired
	fresh.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "NEW-TITLE"}}
	source.set(fresh)
	newerDone := make(chan error, 1)
	go func() {
		independent := postgres.NewDevices(f.newPool(t), 5*time.Second)
		next, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), independent)
		if err != nil {
			newerDone <- err
			return
		}
		service := commerce.NewCommerceService(newRegistryWith(t, next),
			r1CoordinatedMappings{maps, independent}, source, nil)
		_, err = service.SyncProduct(f.ctx, "shopify-main", f.product)
		newerDone <- err
	}()

	releaseOld()
	if err := <-oldDone; err == nil {
		t.Fatal("uncertain settlement must surface as an error")
	}
	newerErr := <-newerDone
	if newerErr == nil || !strings.Contains(newerErr.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
		t.Fatalf("newer synchronization must be blocked, got %v", newerErr)
	}
	if _, active := f.barrierActive(); !active {
		t.Fatal("barrier must survive the coordinator sequence")
	}

	// Coordinator recreation: the barrier persists and still blocks.
	f.restoreHarness()
	before := len(f.h.recorded())
	restarted := postgres.NewDevices(f.newPool(t), 5*time.Second)
	third, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), restarted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.UpsertProduct(f.ctx, f.mutationRequest()); err == nil ||
		!strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
		t.Fatalf("recreated coordinator must stay blocked, got %v", err)
	}
	if len(f.h.recorded()) != before {
		t.Fatal("blocked synchronization reached the provider")
	}

	// A current remote GET (and elapsed time) never clears the barrier.
	if _, err := f.provider.loadProduct(f.ctx, "gid://shopify/Product/"+f.external); err != nil {
		t.Fatal(err)
	}
	if _, active := f.barrierActive(); !active {
		t.Fatal("current remote state cleared the barrier")
	}

	// Only explicit confirmed settlement of the exact operation resolves.
	operation, active := f.barrierActive()
	if !active {
		t.Fatal("barrier missing before resolution")
	}
	resolved := len(f.h.recorded())
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "remote_completed", true); err != nil {
		t.Fatal(err)
	}
	if len(f.h.recorded()) != resolved {
		t.Fatal("resolution must not resend anything")
	}
	if _, active := f.barrierActive(); active {
		t.Fatal("confirmed settlement must resolve the exact operation")
	}

	// A subsequent fresh synchronization rereads canonical desired state
	// and converges.
	service := commerce.NewCommerceService(newRegistryWith(t, third),
		r1CoordinatedMappings{maps, restarted}, source, nil)
	if _, err := service.SyncProduct(f.ctx, "shopify-main", f.product); err != nil {
		t.Fatal(err)
	}
	state := f.h.remoteState(f.external)
	if state.title != "NEW-TITLE" {
		t.Fatalf("fresh synchronization did not converge: %+v", state)
	}
}

// §7 operator-resolution safety.
func TestR2OperatorResolutionSafety(t *testing.T) {
	f := newR2Fixture(t)
	f.interceptMutation(t, "MoonlightProductUpdate", true, 200,
		`{"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`)
	if _, err := f.provider.UpsertProduct(f.ctx, f.mutationRequest()); err == nil {
		t.Fatal("expected uncertain failure")
	}
	f.restoreHarness()
	operation, active := f.barrierActive()
	if !active {
		t.Fatal("barrier missing")
	}

	// Unconfirmed resolution fails.
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "remote_completed", false); err == nil {
		t.Fatal("unconfirmed resolution accepted")
	}
	// Invalid resolution vocabulary fails.
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "whatever", true); err == nil {
		t.Fatal("invalid resolution accepted")
	}
	// Wrong operation id fails and clears nothing.
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product,
		"019c0000-0000-7000-8000-0000000000ff", "remote_completed", true); err == nil {
		t.Fatal("wrong operation id accepted")
	}
	if _, active := f.barrierActive(); !active {
		t.Fatal("wrong resolution cleared the barrier")
	}

	// Confirmed resolution resolves the exact operation and replays safely.
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "remote_not_applied", true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "remote_not_applied", true); err != nil {
		t.Fatalf("identical confirmed resolution must replay safely: %v", err)
	}
	// Contradictory resolution fails.
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "remote_completed", true); err == nil {
		t.Fatal("contradictory resolution accepted")
	}
	// Resolution never resends and history stays durable.
	if got := len(f.h.recorded()); got == 0 {
		t.Fatal("no provider calls recorded")
	}
	var resolution string
	if err := f.pool.QueryRow(f.ctx,
		"SELECT resolution FROM commerce_product_mutation_barriers WHERE operation_id=$1 AND state='resolved'", operation,
	).Scan(&resolution); err != nil {
		t.Fatalf("resolution history not durable: %v", err)
	}
	if resolution != "remote_not_applied" {
		t.Fatalf("history = %q", resolution)
	}

	// Resolving an older operation can never clear a newer barrier.
	f.interceptMutation(t, "MoonlightProductUpdate", true, 200,
		`{"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`)
	if _, err := f.provider.UpsertProduct(f.ctx, f.mutationRequest()); err == nil {
		t.Fatal("expected uncertain failure")
	}
	f.restoreHarness()
	newerOperation, active := f.barrierActive()
	if !active || newerOperation == operation {
		t.Fatalf("newer barrier missing: %q", newerOperation)
	}
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "remote_completed", true); err == nil {
		t.Fatal("stale resolution of an older operation accepted")
	}
	if current, active := f.barrierActive(); !active || current != newerOperation {
		t.Fatalf("older resolution disturbed the newer barrier: %q %v", current, active)
	}
}
