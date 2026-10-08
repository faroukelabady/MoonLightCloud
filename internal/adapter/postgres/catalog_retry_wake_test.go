package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/google/uuid"
)

// NextCatalogRetry is the projector's scheduled-retry wake hint: the
// earliest FUTURE retry for exactly one processor and its event types.
func TestNextCatalogRetryScopesProcessorAndFuture(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	retry := func(eventID, processor string, at time.Time) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status, attempt_count, next_attempt_at, last_error_code)
			VALUES ($1,$2,'retry',1,$3,'CATALOG_DEPENDENCY_WAIT')
			ON CONFLICT (event_id, processor) DO UPDATE SET status='retry', next_attempt_at=excluded.next_attempt_at`, eventID, processor, at); err != nil {
			t.Fatal(err)
		}
	}
	next := func(processor, eventType string) (time.Time, bool) {
		t.Helper()
		at, ok, err := d.NextCatalogRetry(ctx, processor, eventType, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return at, ok
	}

	if _, ok := next(catalog.ProcessorCategoryProjectionV1, catalog.EventCategorySnapshotV1); ok {
		t.Fatal("empty schedule reported a retry")
	}
	v1, v2 := uuid.NewString(), uuid.NewString()
	f.ingest(t, f.devA, f.credA, v1, catalog.EventCategorySnapshotV1, categoryPayload(uuid.NewString(), "active", map[string]string{"ar": "أ"}, nil, 1))
	f.ingest(t, f.devA, f.credA, v2, catalog.EventCategorySnapshotV2, categoryV2Payload(uuid.NewString(), "active", map[string]string{"ar": "ب", "en": "B"}, nil, 1, true))
	base := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)

	// Category v1 and v2 share one processor stream.
	retry(v1, catalog.ProcessorCategoryProjectionV1, base.Add(20*time.Second))
	retry(v2, catalog.ProcessorCategoryProjectionV1, base.Add(10*time.Second))
	if at, ok := next(catalog.ProcessorCategoryProjectionV1, catalog.EventCategorySnapshotV1); !ok || !at.Equal(base.Add(10*time.Second)) {
		t.Fatalf("earliest category retry = %v %v, want %v", at, ok, base.Add(10*time.Second))
	}
	// Another processor's schedule is invisible.
	if _, ok := next(catalog.ProcessorTagProjectionV1, catalog.EventTagSnapshotV1); ok {
		t.Fatal("tag processor saw category retries")
	}
	// Already-due retries belong to PendingCatalogEvents, not the wake hint.
	retry(v2, catalog.ProcessorCategoryProjectionV1, time.Now().Add(-time.Minute))
	if at, ok := next(catalog.ProcessorCategoryProjectionV1, catalog.EventCategorySnapshotV2); !ok || !at.Equal(base.Add(20*time.Second)) {
		t.Fatalf("future-only retry = %v %v, want %v", at, ok, base.Add(20*time.Second))
	}
	// Non-retry states never schedule a wake.
	if _, err := f.pool.Exec(ctx, `UPDATE sync_event_processing SET status='blocked' WHERE event_id=$1`, v1); err != nil {
		t.Fatal(err)
	}
	if _, ok := next(catalog.ProcessorCategoryProjectionV1, catalog.EventCategorySnapshotV1); ok {
		t.Fatal("blocked row scheduled a wake")
	}
}
