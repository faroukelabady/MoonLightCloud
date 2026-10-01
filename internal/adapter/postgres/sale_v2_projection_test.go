package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// Phase 8C Cloud v2: historical tag projection, capture markers,
// idempotent replay, cross-version arbitration, and verbatim snapshots.

func (e *saleEnv) ingestV2(t *testing.T, eventID, payload string) isync.BatchResult {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v2","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, payload)
	res, err := e.syncSvc.Ingest(context.Background(), e.devID, e.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return res
}

func v2Fixture(t *testing.T, tags map[string]any) string {
	t.Helper()
	raw := fixture(t, "sale_usd.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	lines := m["lines"].([]any)
	for _, entry := range lines {
		entry.(map[string]any)["tags"] = []any{
			map[string]any{
				"tag_id": "aaaaaaaa-0000-4000-8000-000000000001",
				"slug":   "horse", "name_ar": "حصان", "name_en": "Horse",
			},
			map[string]any{
				"tag_id": "aaaaaaaa-0000-4000-8000-000000000002",
				"slug":   "animals", "name_ar": "حيوانات", "name_en": "Animals",
			},
		}
	}
	_ = tags
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSaleV2ProjectsTagsAndCapture(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "33333333-3333-7222-8222-222222222222"
	res := env.ingestV2(t, eventID, v2Fixture(t, nil))
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	var capture *bool
	if err := env.pool.QueryRow(context.Background(),
		`SELECT tag_capture FROM sales_projection WHERE sale_id = '11111111-1111-4111-8111-111111111111'`).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if capture == nil || !*capture {
		t.Fatal("v2 projection marks tag capture")
	}
	rows, err := env.pool.Query(context.Background(),
		`SELECT tag_id::text, slug, name_ar, name_en FROM sale_item_tag_snapshots ORDER BY tag_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type tagRow struct{ id, slug, ar, en string }
	var got []tagRow
	for rows.Next() {
		var r tagRow
		if err := rows.Scan(&r.id, &r.slug, &r.ar, &r.en); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 || got[0].slug != "horse" || got[0].ar != "حصان" ||
		got[1].slug != "animals" || got[1].en != "Animals" {
		t.Fatalf("verbatim tag snapshots: %+v", got)
	}
}

func TestSaleV1ProjectsUnknownCapture(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "44444444-4444-7222-8222-222222222222"
	res := env.ingest(t, eventID, fixture(t, "sale_usd.json"))
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	var capture *bool
	if err := env.pool.QueryRow(context.Background(),
		`SELECT tag_capture FROM sales_projection WHERE sale_id = '11111111-1111-4111-8111-111111111111'`).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if capture != nil {
		t.Fatal("v1 projection leaves tag capture unknown (NULL), never fabricated")
	}
	if n := saleCount(t, env.pool, "sale_item_tag_snapshots"); n != 0 {
		t.Fatalf("no tag rows for v1: %d", n)
	}
}

func TestSaleV2EmptyTagsProjectsCapturedEmpty(t *testing.T) {
	env := openSaleEnv(t)
	projectSaleV2(t, env, "bbbbbbbb-bbbb-4bbb-8bbb-000000000004", "2026-09-20T10:00:00Z", nil)
	var capture *bool
	if err := env.pool.QueryRow(context.Background(), `SELECT tag_capture FROM sales_projection`).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if capture == nil || !*capture {
		t.Fatal("valid empty v2 array must be captured-empty")
	}
	if n := saleCount(t, env.pool, "sale_item_tag_snapshots"); n != 0 {
		t.Fatalf("empty capture has %d tags", n)
	}
}

func TestPreviouslyAcceptedMalformedV2BlocksWithoutProjection(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	var payload map[string]any
	if err := json.Unmarshal([]byte(v2Fixture(t, nil)), &payload); err != nil {
		t.Fatal(err)
	}
	for _, entry := range payload["lines"].([]any) {
		entry.(map[string]any)["tags"] = nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	eventID := "aaaaaaaa-aaaa-7aaa-8aaa-000000000007"
	// Seed a pre-remediation accepted event; new transport would reject it.
	if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash) VALUES ($1,$2,'sale.finalized.v2',now(),$3,'\x01')`, eventID, env.devID, string(raw)); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	record, ok, err := store.LoadSaleEvent(ctx, eventID)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	result, err := store.ProjectSaleV2(ctx, record, time.Now())
	if err != nil || result.Outcome != sale.OutcomeBlocked || result.ErrorCode != ErrValidation {
		t.Fatalf("durable validation outcome: %+v %v", result, err)
	}
	for _, table := range []string{"sale_event_ownership", "sales_projection", "sale_item_tag_snapshots"} {
		if n := saleCount(t, env.pool, table); n != 0 {
			t.Fatalf("%s has partial state: %d", table, n)
		}
	}
	if n := saleCount(t, env.pool, "sync_events"); n != 1 {
		t.Fatalf("accepted history must remain: %d", n)
	}
}

func TestSaleV2MalformedTagsRejectBeforeOwnershipOrProjection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing", func(line map[string]any) { delete(line, "tags") }},
		{"null", func(line map[string]any) { line["tags"] = nil }},
		{"mixed lines", nil},
		{"invalid slug", func(line map[string]any) { line["tags"].([]any)[0].(map[string]any)["slug"] = "bad slug/!" }},
		{"case duplicate identity", func(line map[string]any) {
			tags := line["tags"].([]any)
			original := tags[0].(map[string]any)
			duplicate := map[string]any{}
			for key, value := range original {
				duplicate[key] = value
			}
			duplicate["tag_id"] = "AAAAAAAA-0000-4000-8000-000000000001"
			line["tags"] = append(tags, duplicate)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := openSaleEnv(t)
			var m map[string]any
			if err := json.Unmarshal([]byte(v2Fixture(t, nil)), &m); err != nil {
				t.Fatal(err)
			}
			if tc.name == "mixed lines" {
				lines := m["lines"].([]any)
				second := map[string]any{}
				for key, value := range lines[0].(map[string]any) {
					second[key] = value
				}
				second["sale_item_id"] = "33333333-3333-4333-8333-333333333334"
				delete(second, "tags")
				m["lines"] = append(lines, second)
				for _, value := range m["totals"].(map[string]any) {
					money := value.(map[string]any)
					money["amount_minor"] = money["amount_minor"].(float64) * 2
				}
				for _, value := range m["payments"].([]any) {
					money := value.(map[string]any)["amount"].(map[string]any)
					money["amount_minor"] = money["amount_minor"].(float64) * 2
				}
			} else {
				for _, entry := range m["lines"].([]any) {
					tc.mutate(entry.(map[string]any))
				}
			}
			payload, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"events":[{"event_id":"aaaaaaaa-aaaa-7aaa-8aaa-000000000009","event_type":"sale.finalized.v2","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`, payload)
			result, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
			if err == nil && len(result.Events) > 0 && result.Events[0].Status == "accepted" {
				t.Fatalf("accepted malformed v2: %+v", result)
			}
			for _, table := range []string{"sync_events", "sale_event_ownership", "sales_projection", "sale_item_tag_snapshots"} {
				if n := saleCount(t, env.pool, table); n != 0 {
					t.Fatalf("%s has %d rows after rejection", table, n)
				}
			}
		})
	}
}

func TestSaleV2ReplayIdempotent(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "55555555-5555-7222-8222-222222222222"
	payload := v2Fixture(t, nil)
	if res := env.ingestV2(t, eventID, payload); res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sale_item_tag_snapshots") == 2 }, "tag snapshots")
	// Same event redelivered (already_accepted path at ingest or replay):
	// still exactly one sale and one tag set.
	env.drain(t)
	time.Sleep(500 * time.Millisecond)
	if n := saleCount(t, env.pool, "sales_projection"); n != 1 {
		t.Fatalf("one sale: %d", n)
	}
	if n := saleCount(t, env.pool, "sale_item_tag_snapshots"); n != 2 {
		t.Fatalf("one tag set: %d", n)
	}
}

func TestSaleV1V2ArbitrationConflict(t *testing.T) {
	env := openSaleEnv(t)
	v1ID := "66666666-6666-7222-8222-222222222222"
	if res := env.ingest(t, v1ID, fixture(t, "sale_usd.json")); res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Same sale_id as v2: ownership already belongs to the v1 event.
	v2ID := "77777777-7777-7222-8222-222222222222"
	if res := env.ingestV2(t, v2ID, v2Fixture(t, nil)); res.Events[0].Status != "accepted" {
		t.Fatalf("ingest accepts (projection decides), got %+v", res)
	}
	env.drain(t)
	time.Sleep(time.Second)
	var code *string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT last_error_code FROM sync_event_processing WHERE event_id = $1 AND processor = 'sale_projection.v2'`,
		v2ID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code == nil || *code != "SALE_ID_CONFLICT" {
		t.Fatalf("cross-version conflict blocked, got %v", code)
	}
	if n := saleCount(t, env.pool, "sale_item_tag_snapshots"); n != 0 {
		t.Fatalf("loser writes no tags: %d", n)
	}
}
