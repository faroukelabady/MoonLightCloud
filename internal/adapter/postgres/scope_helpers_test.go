package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Shared Store-scope fixture helpers.

func r4Payloads(t *testing.T, f *scopeFixture) string {
	t.Helper()
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT jsonb_agg(jsonb_build_array(event_id,payload,payload_hash) ORDER BY event_id)::text FROM sync_events`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// seedStoreProduct projects minimal Store-owned catalog products (with one
// shared category and provenance event) for tests that only need a
// product's identity and Store, not the full projector chain.
func seedStoreProduct(t *testing.T, env *saleEnv, productIDs ...string) {
	t.Helper()
	ctx := context.Background()
	event, category := uuid.NewString(), uuid.NewString()
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash, store_id)
		VALUES ($1, $2, 'catalog.product.snapshot.v2', now(), '{}', '\x00', $3)`, event, env.devID, env.storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		VALUES ($1, 'active', 'Root', 1, $2, $3, '\x00', now(), $4)`, category, event, env.devID, env.storeID); err != nil {
		t.Fatal(err)
	}
	for _, productID := range productIDs {
		if _, err := env.pool.Exec(ctx, `INSERT INTO catalog_products (product_id, name_ar, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
			VALUES ($1, 'Seed', $2, true, 1, $3, $4, '\x00', now(), $5)`, productID, category, event, env.devID, env.storeID); err != nil {
			t.Fatal(err)
		}
	}
}
