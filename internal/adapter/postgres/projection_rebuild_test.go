package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestProjectionRebuildFromInbox proves derived Sale projections rebuild
// exactly from immutable sync_events (CRIT-01 repair path): clear derived
// tables, reset processing state, re-run the projector, and require the
// representation to match the original correct projection exactly.
func TestProjectionRebuildFromInbox(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	e1 := "22222222-2222-7222-8222-222222222222"
	e2 := "33333333-3333-7333-8333-333333333333"
	env.ingest(t, e1, fixture(t, "sale_usd.json"))
	env.ingest(t, e2, fixture(t, "sale_egp.json"))
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 2 }, "initial projections")
	_ = ctx

	snapshot := dumpProjections(t, env)
	if len(snapshot) == 0 {
		t.Fatal("snapshot must not be empty")
	}

	// Clear derived state only; sync_events (source of truth) untouched.
	if _, err := env.pool.Exec(context.Background(),
		`DELETE FROM sales_projection`); err != nil {
		t.Fatal(err)
	}
	// Children cascade; processing rows for the events go back to pending.
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
		 attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
		 WHERE processor='sale_projection.v1'`); err != nil {
		t.Fatal(err)
	}
	if n := saleCount(t, env.pool, "sync_events"); n != 2 {
		t.Fatalf("source events must be retained, got %d", n)
	}

	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 2 }, "rebuilt projections")

	rebuilt := dumpProjections(t, env)
	if snapshot != rebuilt {
		t.Fatalf("rebuild mismatch:\nbefore: %s\nafter:  %s", snapshot, rebuilt)
	}
	// Processing states are processed again.
	var processed int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_event_processing WHERE processor='sale_projection.v1' AND status='processed'`).Scan(&processed); err != nil || processed != 2 {
		t.Fatalf("want 2 processed, got %d (%v)", processed, err)
	}
}

// dumpProjections renders all derived projection rows deterministically
// (ordered) for exact rebuild comparison.
func dumpProjections(t *testing.T, env *saleEnv) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var b strings.Builder
	dump := func(query string) {
		rows, err := env.pool.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		fields := rows.FieldDescriptions()
		for rows.Next() {
			vals := make([]any, len(fields))
			ptrs := make([]any, len(fields))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%v\n", vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	dump(`SELECT sale_id::text, source_event_id::text, source_device_id::text, sale_number, channel,
		occurred_at::text, paid_at::text, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en,
		shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en, cashier_id, cashier_name, currency,
		subtotal_minor, discount_minor, tax_minor, total_minor, fx_base, fx_quote, fx_rate, fx_rate_microrate,
		received_at::text FROM sales_projection ORDER BY sale_id::text`)
	dump(`SELECT sale_id::text, sale_item_id::text, position, product_id::text, variant_id::text, sku, product_name,
		width_cm, height_cm, quantity, unit_price_minor, unit_currency, cost_minor, cost_currency,
		line_total_minor, line_currency FROM sale_lines_projection ORDER BY sale_id::text, sale_item_id::text`)
	dump(`SELECT sale_id::text, position, method, amount_minor, amount_currency, change_minor, change_currency,
		transaction_ref FROM sale_payments_projection ORDER BY sale_id::text, position`)
	dump(`SELECT sale_id::text, sale_item_id::text, classification_kind, classification_id::text, name_ar, name_en, position
		FROM sale_line_classifications_projection ORDER BY sale_id::text, sale_item_id::text, classification_kind, classification_id::text`)
	return b.String()
}
