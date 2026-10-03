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
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMutationBarrierResponseEvidence(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		blocked    bool
	}{
		{"empty", `{}`, 200, true},
		{"missing_root", `{"data":{}}`, 200, true},
		{"null_primary", `{"data":{"productUpdate":{"product":null,"userErrors":[]}}}`, 200, true},
		{"null_errors", `{"data":{"productUpdate":{"product":{"id":"gid://shopify/Product/1"},"userErrors":null}}}`, 200, true},
		{"invalid_error", `{"data":{"productUpdate":{"product":null,"userErrors":[null]}}}`, 200, true},
		{"malformed", `{"data":`, 200, true},
		{"trailing", `{"data":{"productUpdate":{"product":{"id":"gid://shopify/Product/1"},"userErrors":[]}}} garbage`, 200, true},
		{"server_failure", `{"errors":[{"message":"unknown execution"}]}`, 503, true},
		{"explicit_denial", `{"errors":[{"message":"refused"}]}`, 403, false},
		{"explicit_throttle", `{"errors":[{"message":"throttled"}]}`, 429, false},
		{"validation", `{"data":{"productUpdate":{"product":null,"userErrors":[{"message":"invalid input"}]}}}`, 200, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database := testutil.Isolated(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool, err := pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			store := postgres.NewDevices(pool, 5*time.Second)
			h := newHarness(t)
			p, err := NewShopifyProvider(testConfig(), harnessClient(t, h), store)
			if err != nil {
				t.Fatal(err)
			}
			product := "019c0000-0000-7000-8000-000000000011"
			created, err := p.UpsertProduct(ctx, upsertRequest(product, "PAP-001", true))
			if err != nil {
				t.Fatal(err)
			}
			h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var request gqlRequest
				_ = json.Unmarshal(raw, &request)
				if operationName(request.Query) != "MoonlightProductUpdate" {
					h.serve(w, r)
					return
				}
				var fingerprint, state string
				if err := pool.QueryRow(ctx, "SELECT request_fingerprint,state FROM commerce_product_mutation_barriers WHERE provider_key='shopify-main' AND product_id=$1 AND state IN ('in_flight','uncertain')", product).Scan(&fingerprint, &state); err != nil {
					t.Error("no committed barrier before provider send", err)
				}
				if fingerprint != fmt.Sprintf("%x", sha256.Sum256(raw)) || state != "in_flight" {
					t.Error("wrong request evidence", fingerprint, state)
				}
				if tc.blocked {
					h.serve(httptest.NewRecorder(), r)
				} // apply, then lose completion evidence
				w.Header().Set("X-Shopify-API-Version", "2026-10")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			req := staleRequest(created.ExternalProductID)
			req.ProductID = product
			if _, err := p.UpsertProduct(ctx, req); err == nil {
				t.Fatal("response failure ignored")
			}
			status, err := store.GetProductMutationBarrier(ctx, "shopify-main", product)
			if err != nil {
				t.Fatal(err)
			}
			if (status.OperationID != "") != tc.blocked {
				t.Fatalf("wrong barrier outcome: %+v", status)
			}
			if tc.blocked {
				before := len(h.recorded())
				next, _ := NewShopifyProvider(testConfig(), harnessClient(t, h), store)
				if _, err := next.UpsertProduct(ctx, req); err == nil || !strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
					t.Fatal("uncertain write automatically retried", err)
				}
				if len(h.recorded()) != before {
					t.Fatal("blocked retry reached provider")
				}
			}
		})
	}
}

func TestMutationBarrierPersistenceFaults(t *testing.T) {
	for _, stage := range []string{"admission", "completion"} {
		t.Run(stage, func(t *testing.T) {
			database := testutil.Isolated(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool, err := pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			store := postgres.NewDevices(pool, 5*time.Second)
			h := newHarness(t)
			p, _ := NewShopifyProvider(testConfig(), harnessClient(t, h), store)
			triggerEvent := "INSERT"
			if stage == "completion" {
				triggerEvent = "DELETE"
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_barrier_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test-only fault'; END $$;`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `CREATE TRIGGER reject_barrier_write BEFORE `+triggerEvent+` ON commerce_product_mutation_barriers FOR EACH ROW EXECUTE FUNCTION reject_barrier_write()`); err != nil {
				t.Fatal(err)
			}
			product := "019c0000-0000-7000-8000-000000000011"
			if _, err := p.UpsertProduct(ctx, upsertRequest(product, "PAP-001", true)); err == nil {
				t.Fatal("persistence fault ignored")
			}
			status, err := store.GetProductMutationBarrier(ctx, "shopify-main", product)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "admission" {
				if h.creations() != 0 || status.OperationID != "" {
					t.Fatal("failed admission permitted write", h.creations(), status)
				}
			} else {
				if h.creations() != 1 || status.OperationID == "" {
					t.Fatal("failed completion lost durable evidence", h.creations(), status)
				}
				if _, err := p.UpsertProduct(ctx, upsertRequest(product, "PAP-001", true)); err == nil {
					t.Fatal("unknown completion retried")
				}
				if h.creations() != 1 {
					t.Fatal("completion failure duplicated remote creation")
				}
			}
		})
	}
}
