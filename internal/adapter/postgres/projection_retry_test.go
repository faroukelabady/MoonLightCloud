package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// HIGH-04: manual retry returns blocked/retry rows to pending with NULL
// next_attempt_at, immediately discoverable; blocked rows are otherwise
// never returned (no hot-loop).
func TestManualRetryDiscoverability(t *testing.T) {
	env := openSaleEnv(t)
	payload := fixture(t, "sale_egp.json")
	blockedID := "22222222-2222-7222-8222-222222222222"
	retryID := "33333333-3333-7333-8333-333333333333"
	env.ingest(t, blockedID, payload)
	env.ingest(t, retryID, payload)
	store := NewDevices(env.pool, 5*time.Second)
	ctx := context.Background()

	// Force states: blocked (deterministic) and retry (future due).
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO sync_event_processing (event_id, processor, status, attempt_count, last_error_code)
		 VALUES ($1,'sale_projection.v1','blocked',1,'SALE_ID_CONFLICT'),($2,'sale_projection.v1','retry',3,'PROJECTION_FAILED')`,
		blockedID, retryID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET next_attempt_at = now() + interval '1 hour' WHERE event_id=$1`, retryID); err != nil {
		t.Fatal(err)
	}

	// Blocked is not discoverable (no hot-loop); future retry is not due.
	due, err := store.PendingSaleEvents(ctx, sale.ProcessorSaleProjectionV1, 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range due {
		if id == blockedID || id == retryID {
			t.Fatalf("%s must not be discoverable before manual retry", id)
		}
	}

	// Manual retry both.
	if err := store.ResetProcessing(ctx, sale.ProcessorSaleProjectionV1, blockedID); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetProcessing(ctx, sale.ProcessorSaleProjectionV1, retryID); err != nil {
		t.Fatal(err)
	}
	var status string
	var next *time.Time
	for _, id := range []string{blockedID, retryID} {
		if err := env.pool.QueryRow(ctx,
			`SELECT status, next_attempt_at FROM sync_event_processing WHERE event_id=$1`, id).Scan(&status, &next); err != nil {
			t.Fatal(err)
		}
		if status != "pending" || next != nil {
			t.Fatalf("%s: want pending/NULL, got %q/%v", id, status, next)
		}
	}
	due, err = store.PendingSaleEvents(ctx, sale.ProcessorSaleProjectionV1, 25)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, id := range due {
		seen[id] = true
	}
	if !seen[blockedID] || !seen[retryID] {
		t.Fatalf("manual retry must be immediately discoverable: %v", due)
	}
}

// HIGH-04: missing and pending rows are discoverable; processed never is.
func TestDiscoveryStateSemantics(t *testing.T) {
	env := openSaleEnv(t)
	payload := fixture(t, "sale_egp.json")
	missingID := "22222222-2222-7222-8222-222222222222"
	env.ingest(t, missingID, payload)
	store := NewDevices(env.pool, 5*time.Second)
	ctx := context.Background()
	due, err := store.PendingSaleEvents(ctx, sale.ProcessorSaleProjectionV1, 25)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range due {
		if id == missingID {
			found = true
		}
	}
	if !found {
		t.Fatal("missing processing row must be discoverable")
	}
	// Claim as pending explicitly: still discoverable.
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO sync_event_processing (event_id, processor, status) VALUES ($1,'sale_projection.v1','pending')`, missingID); err != nil {
		t.Fatal(err)
	}
	due, err = store.PendingSaleEvents(ctx, sale.ProcessorSaleProjectionV1, 25)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, id := range due {
		if id == missingID {
			found = true
		}
	}
	if !found {
		t.Fatal("pending must be discoverable")
	}
	// Processed: never discoverable.
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET status='processed' WHERE event_id=$1`, missingID); err != nil {
		t.Fatal(err)
	}
	due, err = store.PendingSaleEvents(ctx, sale.ProcessorSaleProjectionV1, 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range due {
		if id == missingID {
			t.Fatal("processed must not be discoverable")
		}
	}
}

// HIGH-04 §23: backoff arithmetic never overflows; 1h policy retained.
func TestBackoffOverflowSafe(t *testing.T) {
	if got := sale.Backoff(1 << 30); got != time.Hour {
		t.Fatalf("huge attempt must cap at 1h, got %v", got)
	}
	if got := sale.Backoff(-5); got != 5*time.Second {
		t.Fatalf("negative attempt must yield base delay, got %v", got)
	}
	if got := sale.Backoff(1000000); got != time.Hour {
		t.Fatalf("massive attempt must cap at 1h, got %v", got)
	}
	// Policy points preserved.
	if sale.Backoff(0) != 5*time.Second || sale.Backoff(1) != 10*time.Second || sale.Backoff(11) != time.Hour {
		t.Fatal("backoff policy changed")
	}
}
