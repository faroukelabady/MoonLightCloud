package shopify

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The response carries both execution evidence and errors. Retaining the
// returned Product must prevent a second create, including for an older
// receipt which discarded that identity before this fix.
func TestF07FailedBundleProductEvidence(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new-receipt"
		if legacy {
			name = "legacy-empty-product"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, database, provider := newReceiptStack(t)
			pool, err := pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { pool.Close() }()
			p := provider(&pool)
			req := upsertRequest("019c0000-0000-7000-8000-0000000000e1", "F07-PRODUCT", true)
			req.Product.Configurations = []commerce.CommerceConfiguration{
				frameConfig("019c0000-0000-7000-8000-0000000000e2", "classic", "black", "Classic", "Black", 1000, nil, true),
			}
			f.operationErrorWithProduct = true
			if _, err := p.UpsertProduct(ctx, req); err == nil {
				t.Fatal("operation errors must not be reported as success")
			}
			var operation, state, product string
			if err := pool.QueryRow(ctx, `SELECT provider_operation_id, async_state, coalesce(async_product_id, '')
				FROM commerce_product_mutation_barriers WHERE async_role='bundle_create'`).Scan(&operation, &state, &product); err != nil {
				t.Fatal(err)
			}
			known := f.operations[operation]["product"].(map[string]any)["id"].(string)
			if state != "failed" || product != known {
				t.Fatalf("failed receipt lost returned Product evidence: state=%s preserved=%v", state, product == known)
			}
			if legacy {
				// Isolated test data models the previously shipped writer.
				if _, err := pool.Exec(ctx, `UPDATE commerce_product_mutation_barriers SET async_product_id=''
					WHERE provider_operation_id=$1`, operation); err != nil {
					t.Fatal(err)
				}
			}
			f.operationErrorWithProduct = false
			req.OperationKey = "corrected-after-product"
			req.CatalogRevision++
			changed := "Corrected Style"
			req.Product.Configurations[0].StyleNameEN = &changed
			req.Product.Configurations[0].ConfigurationRevision++
			pool.Close()
			pool, err = pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			p = provider(&pool)
			if _, err := p.UpsertProduct(ctx, req); err == nil {
				t.Fatal("returned Product must block a corrected fresh create")
			}
			if f.creates != 1 || len(f.resources) != 2 {
				t.Fatalf("additional bundle created: creates=%d resources=%d", f.creates, len(f.resources))
			}
			if err := pool.QueryRow(ctx, `SELECT async_state, coalesce(async_product_id, '')
				FROM commerce_product_mutation_barriers WHERE provider_operation_id=$1`, operation).Scan(&state, &product); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || product != known {
				t.Fatal("failed receipt must retain/recover known Product without status regression")
			}
			for _, mutation := range []struct{ state, product string }{
				{"failed", ""},
				{"failed", "gid://shopify/Product/999999"},
				{"completed", known},
			} {
				err := p.coordinator.WithProductSync(ctx, p.key, req.ProductID, func(held context.Context) error {
					store, err := commerce.ProductAsyncReceipts(held)
					if err != nil {
						return err
					}
					return store.FinishAsync(held, operation, mutation.state, mutation.product)
				})
				if err == nil {
					t.Fatal("terminal receipt must reject evidence erasure, replacement and status regression")
				}
			}
			// Once persisted, later provider drift cannot erase this evidence.
			f.operations[operation]["product"] = nil
			if _, err := p.UpsertProduct(ctx, req); err == nil || f.creates != 1 {
				t.Fatal("known durable Product evidence must continue to fence fresh creation")
			}
		})
	}
}

// A legacy empty-ID failure is not itself sufficient to authorize a
// corrected create: missing, malformed or changed operation evidence
// must stay fenced.
func TestF07LegacyFailureRequiresNoProductEvidence(t *testing.T) {
	for _, scenario := range []string{"active", "missing-product", "invalid-product", "no-error", "operation-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f, database, provider := newReceiptStack(t)
			pool, err := pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			p := provider(&pool)
			req := upsertRequest("019c0000-0000-7000-8000-0000000000f1", "F07-UNKNOWN", true)
			req.Product.Configurations = []commerce.CommerceConfiguration{
				frameConfig("019c0000-0000-7000-8000-0000000000f2", "classic", "black", "Classic", "Black", 1000, nil, true),
			}
			f.injectOperation, f.injectMessage = "bundle_operation", "original failure"
			if _, err := p.UpsertProduct(ctx, req); err == nil {
				t.Fatal("expected original failure")
			}
			f.injectOperation = ""
			var operation string
			if err := pool.QueryRow(ctx, `SELECT provider_operation_id FROM commerce_product_mutation_barriers
				WHERE async_role='bundle_create'`).Scan(&operation); err != nil {
				t.Fatal(err)
			}
			op := f.operations[operation]
			op["status"], op["product"] = "COMPLETE", nil
			op["userErrors"] = []any{map[string]any{"message": "original failure"}}
			delete(op, "resource")
			switch scenario {
			case "active":
				op["status"], f.pendingReads = "ACTIVE", 1
			case "missing-product":
				delete(op, "product")
			case "invalid-product":
				op["product"] = map[string]any{"id": "invalid"}
			case "no-error":
				op["userErrors"] = []any{}
			case "operation-unavailable":
				delete(f.operations, operation)
			}
			req.OperationKey = "corrected-unknown"
			req.CatalogRevision++
			changed := "Corrected Style"
			req.Product.Configurations[0].StyleNameEN = &changed
			if _, err := p.UpsertProduct(ctx, req); err == nil || f.creates != 1 {
				t.Fatalf("unverified failure must fence corrected creation: success=%v creates=%d", err == nil, f.creates)
			}
		})
	}
}
