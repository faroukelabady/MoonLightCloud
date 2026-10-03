package shopify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Phase 11-R3: settlement evidence for top-level GraphQL errors.
//
// A recognized refusal code alone never clears the durable mutation
// barrier. Automatic settlement requires validated evidence that the
// COMPLETE response is a definitive pre-execution refusal. Per the
// GraphQL request-error result contract, a request refused before
// execution carries errors and no data key at all; any data key
// (including explicit null) describes an execution result, and error
// path entries are field-level execution evidence. Any of those — or
// malformed/contradictory path evidence — fails closed: the barrier is
// retained. These are controlled fault responses, not live Shopify
// behavior.

// r3Case is one settlement-evidence scenario.
type r3Case struct {
	name   string
	body   string
	retain bool // the barrier must remain active afterwards
}

func r3UncertainCases() []r3Case {
	return []r3Case{
		// R3 finding repros: authorization codes with execution evidence.
		{"auth_code_data_null_with_path",
			`{"data":null,"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"},"path":["productUpdate","product","id"]}]}`, true},
		{"auth_code_data_null_without_path",
			`{"data":null,"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"}}]}`, true},
		{"auth_code_execution_path_data_omitted",
			`{"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"},"path":["productUpdate","product","id"]}]}`, true},
		{"auth_code_malformed_path",
			`{"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"},"path":42}]}`, true},
		{"auth_code_contradictory_null_path",
			`{"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"},"path":null}]}`, true},
		{"mixed_execution_evidence_entry",
			`{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}},{"message":"access denied","extensions":{"code":"ACCESS_DENIED"},"path":["productUpdate"]}]}`, true},
		// Existing uncertain classes stay uncertain.
		{"http200_internal_server_error",
			`{"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, true},
		{"http200_unknown_code",
			`{"errors":[{"message":"uncertain execution","extensions":{"code":"UNRECOGNIZED_FAILURE"}}]}`, true},
		{"missing_error_code",
			`{"errors":[{"message":"boom"}]}`, true},
		{"partial_data_plus_internal_error",
			`{"data":{"productUpdate":null},"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, true},
		{"data_null_plus_internal_error",
			`{"data":null,"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, true},
	}
}

// TestR3SettlementEvidenceMatrix runs the mandatory real-PostgreSQL
// regression matrix (§5): barrier+fingerprint before dispatch, retained
// operation afterwards, no replacement operation or implicit resolution,
// recreated provider blocked with zero additional provider calls.
func TestR3SettlementEvidenceMatrix(t *testing.T) {
	for _, tc := range r3UncertainCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newR2Fixture(t)
			f.interceptMutation(t, "MoonlightProductUpdate", true, 200, tc.body)
			req := f.mutationRequest()
			_, err := f.provider.UpsertProduct(f.ctx, req)
			if err == nil {
				t.Fatal("uncertain response ignored")
			}
			if len(err.Error()) > 512 {
				t.Fatalf("unbounded error: %q", err.Error())
			}
			operation, active := f.barrierActive()
			if !active {
				t.Fatal("settlement evidence gap: barrier was released")
			}
			// No replacement operation, no implicit resolution.
			assertNotResolved(t, f, operation)
			before := len(f.h.recorded())
			f.restoreHarness()
			next, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), f.store)
			if err != nil {
				t.Fatal(err)
			}
			_, err = next.UpsertProduct(f.ctx, req)
			if err == nil || !strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
				t.Fatalf("uncertain write must stay blocked, got %v", err)
			}
			if len(f.h.recorded()) != before {
				t.Errorf("blocked retry reached the provider: before=%d after=%d", before, len(f.h.recorded()))
			}
			after, active := f.barrierActive()
			if !active || after != operation {
				t.Fatalf("retry created a replacement operation: %q (was %q)", after, operation)
			}
			assertNotResolved(t, f, operation)
		})
	}
}

// TestR3ValidRefusalReleasesExactOperation proves the approved refusal
// policy still settles when the complete response carries no execution
// evidence: data key absent and no path on any error.
func TestR3ValidRefusalReleasesExactOperation(t *testing.T) {
	releases := []r3Case{
		{"throttled_pre_execution", `{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}}]}`, false},
		{"authorization_refusal", `{"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"}}]}`, false},
	}
	for _, tc := range releases {
		t.Run(tc.name, func(t *testing.T) {
			f := newR2Fixture(t)
			f.interceptMutation(t, "MoonlightProductUpdate", false, 200, tc.body)
			if _, err := f.provider.UpsertProduct(f.ctx, f.mutationRequest()); err == nil {
				t.Fatal("refusal response must surface as an error")
			}
			f.restoreHarness()
			if _, active := f.barrierActive(); active {
				t.Fatal("validated pre-execution refusal must release its operation")
			}
			if _, err := f.provider.UpsertProduct(f.ctx, f.mutationRequest()); err != nil {
				t.Fatalf("released operation must admit the next write: %v", err)
			}
		})
	}
}

// TestR3SuccessfulMutationReleasesOnlyItsExactOperation.
func TestR3SuccessfulMutationReleasesOnlyItsExactOperation(t *testing.T) {
	f := newR2Fixture(t)
	if _, err := f.provider.UpsertProduct(f.ctx, f.mutationRequest()); err != nil {
		t.Fatal(err)
	}
	if _, active := f.barrierActive(); active {
		t.Fatal("successful mutation left a barrier behind")
	}
	// The exact product synchronizes again normally.
	if _, err := f.provider.UpsertProduct(f.ctx, f.mutationRequest()); err != nil {
		t.Fatal(err)
	}
}

// TestR3ReadOnlyRequestsCreateNoBarrier: read-only GraphQL requests keep
// their existing behavior and never touch the mutation barrier.
func TestR3ReadOnlyRequestsCreateNoBarrier(t *testing.T) {
	f := newR2Fixture(t)
	if err := f.provider.ensureShopCurrency(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.provider.loadProduct(f.ctx, "gid://shopify/Product/"+f.external); err != nil {
		t.Fatal(err)
	}
	if _, active := f.barrierActive(); active {
		t.Fatal("read-only request created a barrier")
	}
	var rows int
	if err := f.pool.QueryRow(f.ctx,
		"SELECT count(*) FROM commerce_product_mutation_barriers WHERE provider_key='shopify-main' AND product_id=$1",
		f.product).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	// Only the fixture's create mutation may have written rows (resolved).
	if rows > 1 {
		t.Fatalf("unexpected barrier rows from read-only work: %d", rows)
	}
}

// TestR3DelayedAuthorizationEvidenceRequiresConfirmedSettlement is the
// §7 delayed-effect proof using an uncertain authorization-coded
// response that contains execution evidence. Controlled provider
// simulation — NOT live Shopify behavior. Deterministic channel
// ordering; no sleep timing.
func TestR3DelayedAuthorizationEvidenceRequiresConfirmedSettlement(t *testing.T) {
	f := newR2Fixture(t)
	responded := make(chan struct{})
	settle := make(chan struct{})
	var mutationBody []byte
	f.h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var request gqlRequest
		_ = json.Unmarshal(raw, &request)
		if operationName(request.Query) != "MoonlightProductUpdate" {
			f.h.serve(w, r)
			return
		}
		mutationBody = append([]byte(nil), raw...)
		// Uncertain authorization-coded response WITH execution evidence:
		// settlement cannot be established.
		w.Header().Set("X-Shopify-API-Version", "2026-10")
		w.WriteHeader(200)
		io.WriteString(w, `{"data":null,"errors":[{"message":"access denied","extensions":{"code":"ACCESS_DENIED"},"path":["productUpdate","product","id"]}]}`)
		close(responded)
	})

	// 1) older mutation is received and answers with uncertain evidence.
	oldErr := make(chan error, 1)
	go func() {
		_, err := f.provider.UpsertProduct(f.ctx, f.mutationRequest())
		oldErr <- err
	}()
	<-responded
	if err := <-oldErr; err == nil {
		t.Fatal("uncertain response must surface as an error")
	}
	operation, active := f.barrierActive()
	if !active {
		t.Fatal("execution evidence must retain the barrier")
	}

	// 2) newer independent synchronization attempts to run and is
	//    rejected before provider dispatch.
	maps := newStubMappings()
	source := &r1MutableSource{desired: fixedDesired(f.product, "PAP-001")}
	fresh := source.desired
	fresh.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "NEW-TITLE"}}
	source.set(fresh)
	before := len(f.h.recorded())
	independent := postgres.NewDevices(f.newPool(t), 5*time.Second)
	next, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), independent)
	if err != nil {
		t.Fatal(err)
	}
	service := commerce.NewCommerceService(newRegistryWith(t, next),
		r1CoordinatedMappings{maps, independent}, source, nil)
	_, err = service.SyncProduct(f.ctx, "shopify-main", f.product)
	if err == nil || !strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
		t.Fatalf("newer synchronization must be rejected before dispatch, got %v", err)
	}
	if len(f.h.recorded()) != before {
		t.Fatal("rejected synchronization reached the provider")
	}

	// 3) the simulated OLD remote effect settles only now — after the
	//    error response (controlled provider simulation).
	close(settle)
	<-settle
	replay := httptest.NewRequest(http.MethodPost, "/admin/api/2026-10/graphql.json", bytes.NewReader(mutationBody))
	f.h.serve(httptest.NewRecorder(), replay)

	// 4) coordinator recreation still does not clear uncertainty; a
	//    current GET, elapsed time and restart are not settlement proof.
	if _, active := f.barrierActive(); !active || operation == "" {
		t.Fatal("delayed effect cleared the barrier")
	}
	f.restoreHarness()
	restarted := postgres.NewDevices(f.newPool(t), 5*time.Second)
	third, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), restarted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.loadProduct(f.ctx, "gid://shopify/Product/"+f.external); err != nil {
		t.Fatal(err)
	}
	if _, active := f.barrierActive(); !active {
		t.Fatal("current remote GET cleared the barrier")
	}
	before = len(f.h.recorded())
	if _, err := third.UpsertProduct(f.ctx, f.mutationRequest()); err == nil ||
		!strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
		t.Fatalf("recreated coordinator must stay blocked, got %v", err)
	}
	if len(f.h.recorded()) != before {
		t.Fatal("recreated coordinator reached the provider")
	}

	// 5) only explicit confirmed resolution of the exact operation
	//    permits a fresh canonical synchronization.
	if err := f.store.ResolveProductMutationBarrier(f.ctx, "shopify-main", f.product, operation, "remote_completed", true); err != nil {
		t.Fatal(err)
	}
	if _, active := f.barrierActive(); active {
		t.Fatal("confirmed settlement must resolve the exact operation")
	}
	service = commerce.NewCommerceService(newRegistryWith(t, third),
		r1CoordinatedMappings{maps, restarted}, source, nil)
	if _, err := service.SyncProduct(f.ctx, "shopify-main", f.product); err != nil {
		t.Fatal(err)
	}
	state := f.h.remoteState(f.external)
	if state.title != "NEW-TITLE" {
		t.Fatalf("fresh synchronization did not converge: %+v", state)
	}
}

// assertNotResolved fails when the operation shows any resolution
// evidence (no implicit resolution, durable history intact).
func assertNotResolved(t *testing.T, f *r2Fixture, operation string) {
	t.Helper()
	var resolved int
	query := "SELECT count(*) FROM commerce_product_mutation_barriers WHERE provider_key='shopify-main' AND product_id=$1 AND state='resolved'"
	if operation != "" {
		query += " AND operation_id=$2"
	}
	var err error
	if operation != "" {
		err = f.pool.QueryRow(f.ctx, query, f.product, operation).Scan(&resolved)
	} else {
		err = f.pool.QueryRow(f.ctx, query, f.product).Scan(&resolved)
	}
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 0 {
		t.Fatal("implicit resolution detected")
	}
}
