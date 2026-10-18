package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// This uses the real sync ingress and PostgreSQL processing rows. It keeps
// the Sale projector idle until each freshness state has been observed.
// sale.finalized.v3 is the only sale version (ADR-0055); freshness and the
// dashboard activity feed must see it (they previously listed v1/v2 only).
func TestReportFreshnessAndDashboardActivity(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := NewDevices(env.pool, 5*time.Second)
	add := func(eventID, eventType, saleID string) {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &payload); err != nil {
			t.Fatal(err)
		}
		payload["sale_id"] = saleID
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`, eventID, eventType, raw)
		result, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(body))
		if err != nil || len(result.Events) != 1 || result.Events[0].Status != "accepted" {
			t.Fatalf("ingest %s: %+v %v", eventID, result, err)
		}
	}
	fresh := func(backlog, blocked int) {
		t.Helper()
		got := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "").Freshness
		if got.ProjectionBacklogCount != int64(backlog) || got.BlockedSaleEventCount != int64(blocked) || got.CloudProjectionComplete != (backlog == 0) || got.LatestSaleEventReceivedAt == nil {
			t.Fatalf("backlog=%d blocked=%d: %+v", backlog, blocked, got)
		}
		var newest time.Time
		if err := env.pool.QueryRow(ctx, `SELECT max(received_at) FROM sync_events WHERE event_type = 'sale.finalized.v3'`).Scan(&newest); err != nil {
			t.Fatal(err)
		}
		if !got.LatestSaleEventReceivedAt.Equal(newest) {
			t.Fatalf("newest accepted timestamp: got %v want %v", got.LatestSaleEventReceivedAt, newest)
		}
	}
	firstPending := "aaaaaaaa-aaaa-7aaa-8aaa-000000000001"
	secondPending := "aaaaaaaa-aaaa-7aaa-8aaa-000000000002"
	add(firstPending, "sale.finalized.v3", "bbbbbbbb-bbbb-4bbb-8bbb-000000000001")
	fresh(1, 0)
	// A terminal record for an unrelated processor must not hide backlog.
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status) VALUES ($1, 'return_refund_projection.v1', 'processed')`, firstPending); err != nil {
		t.Fatal(err)
	}
	fresh(1, 0)
	add(secondPending, "sale.finalized.v3", "bbbbbbbb-bbbb-4bbb-8bbb-000000000002")
	fresh(2, 0)
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status, attempt_count)
		VALUES ($1, 'sale_projection.v3', 'retry', 1)`, firstPending); err != nil {
		t.Fatal(err)
	}
	fresh(2, 0)
	if _, err := env.pool.Exec(ctx, `UPDATE sync_event_processing SET status='blocked', last_error_code='SALE_ID_CONFLICT' WHERE event_id=$1 AND processor='sale_projection.v3'`, firstPending); err != nil {
		t.Fatal(err)
	}
	fresh(1, 1)
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status)
		VALUES ($1, 'sale_projection.v3', 'processed')`, secondPending); err != nil {
		t.Fatal(err)
	}
	fresh(0, 1)
	items, err := NewReporting(store.pool, store.timeout).DashboardRecentActivity(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, item := range items {
		if item.EventID == firstPending {
			kinds[item.Kind] = true
		}
	}
	if !kinds["accepted"] || !kinds["blocked"] {
		t.Fatalf("dashboard activity: %+v", items)
	}
	projectedID := projectSaleWithTags(t, env, "bbbbbbbb-bbbb-4bbb-8bbb-000000000003", "2026-09-20T10:00:00Z", nil)
	fresh(0, 1)
	items, err = NewReporting(store.pool, store.timeout).DashboardRecentActivity(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	foundProjected := false
	for _, item := range items {
		if item.EventID == projectedID && item.Kind == "projected" {
			foundProjected = true
		}
	}
	if !foundProjected {
		t.Fatalf("projected sale missing from dashboard: %+v", items)
	}
}

func TestReportFreshnessPendingSaleAlone(t *testing.T) {
	env := openSaleEnv(t)
	result := env.ingest(t, "aaaaaaaa-aaaa-7aaa-8aaa-000000000006", fixture(t, "sale_usd.json"))
	if result.Events[0].Status != "accepted" {
		t.Fatalf("ingest: %+v", result)
	}
	got := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "").Freshness
	if got.ProjectionBacklogCount != 1 || got.CloudProjectionComplete || got.LatestSaleEventReceivedAt == nil {
		t.Fatalf("pending sale: %+v", got)
	}
}
