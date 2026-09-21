package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// TestAdversarialSaleInputs sends authenticated hostile sale payloads and
// requires deterministic safe behavior: 422/409 only, zero new sync_events
// rows on rejection, no panics, no partial commits.
func TestAdversarialSaleInputs(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	base := fixture(t, "sale_usd.json")
	mk := func(id string, fn func(map[string]any)) (string, string) {
		var m map[string]any
		if err := json.Unmarshal([]byte(base), &m); err != nil {
			t.Fatal(err)
		}
		fn(m)
		raw, _ := json.Marshal(m)
		return id, string(raw)
	}
	root2 := func(m map[string]any) {
		line := m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)
		roots := line["roots"].([]any)
		dup := map[string]any{"category_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "name_ar": "x", "name_en": "y"}
		line["roots"] = append(roots, dup)
	}
	_ = root2
	cases := []struct {
		name string
		id   string
		fn   func(map[string]any)
		raw  string
	}{
		{"large exact integers", "a1111111-1111-4111-8111-111111111111", func(m map[string]any) {
			// MaxInt64 money is structurally valid; totals recomputed to stay
			// consistent would overflow elsewhere — here it must fail safely.
			m["totals"].(map[string]any)["total"].(map[string]any)["amount_minor"] = 9223372036854775807
		}, ""},
		{"invalid payment aggregate", "a2222222-2222-4222-8222-222222222222", func(m map[string]any) {
			m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 1
		}, ""},
		{"multiple roots", "a3333333-3333-4333-8333-333333333333", func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)
			roots := line["roots"].([]any)
			dup := map[string]any{"category_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "name_ar": "x", "name_en": "y"}
			line["roots"] = append(roots, dup)
		}, ""},
		{"duplicate classification ids", "a4444444-4444-4444-8444-444444444444", func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)
			roots := line["roots"].([]any)
			line["roots"] = append(roots, roots[0])
		}, ""},
		{"cross-group classification reuse", "a4444445-4444-4444-8444-444444444444", func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)
			rootID := line["roots"].([]any)[0].(map[string]any)["category_id"]
			subs := line["subcategories"].([]any)
			line["subcategories"] = append(subs, map[string]any{"category_id": rootID, "name_ar": "x", "name_en": "y"})
		}, ""},
		{"invalid fx pair", "a5555555-5555-4555-8555-555555555555", func(m map[string]any) {
			m["fx"].(map[string]any)["base"] = "EUR"
			m["fx"].(map[string]any)["quote"] = "USD"
		}, ""},
		{"fractional money", "a6666666-6666-4666-8666-666666666666", func(m map[string]any) {
			m["totals"].(map[string]any)["total"].(map[string]any)["amount_minor"] = 1250.5
		}, ""},
		{"huge exponent number", "a7777777-7777-4777-8777-777777777777", nil,
			`{"sale_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","v":1e1000000}`},
	}
	for _, tc := range cases {
		var payload string
		if tc.raw != "" {
			payload = tc.raw
		} else {
			_, payload = mk(tc.id, tc.fn)
		}
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
			tc.id, payload)
		before := saleCount(t, env.pool, "sync_events")
		_, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(body))
		if err == nil {
			t.Fatalf("%s: hostile input must not ACK", tc.name)
		}
		k := identityKindOf(err)
		if k != apperr.Unprocessable && k != apperr.InvalidInput && k != apperr.TooLarge {
			t.Fatalf("%s: want 4xx deterministic, got %v", tc.name, err)
		}
		if after := saleCount(t, env.pool, "sync_events"); after != before {
			t.Fatalf("%s: zero new rows on reject, got %d→%d", tc.name, before, after)
		}
	}
	// Same-ID mutations after a legitimate accept → deterministic conflict.
	legitID := "b1111111-1111-4111-8111-111111111111"
	if r := env.ingest(t, legitID, fixture(t, "sale_egp.json")); r.Events[0].Status != "accepted" {
		t.Fatalf("legit: %+v", r)
	}
	before := saleCount(t, env.pool, "sync_events")
	mutations := []struct {
		name string
		typ  string
		occ  string
		pay  string
	}{
		{"changed type", "system.test.v1", "2026-09-20T10:00:00Z", fixture(t, "sale_egp.json")},
		{"changed occurred_at", "sale.finalized.v1", "2026-09-21T10:00:00Z", fixture(t, "sale_egp.json")},
		{"large-number variation", "sale.finalized.v1", "2026-09-20T10:00:00Z", bigVariant(t)},
	}
	for _, m := range mutations {
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":%q,"payload":%s}]}`,
			legitID, m.typ, m.occ, m.pay)
		if _, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(body)); err == nil {
			t.Fatalf("%s: must conflict", m.name)
		} else if identityKindOf(err) != apperr.Conflict {
			t.Fatalf("%s: want 409, got %v", m.name, err)
		}
	}
	if after := saleCount(t, env.pool, "sync_events"); after != before {
		t.Fatalf("conflicts must commit nothing: %d→%d", before, after)
	}
}

// bigVariant returns a valid sale payload with an extra exact large integer
// (unknown additive field, tolerated by validation) so the payload hash
// differs while sale semantics stay valid — the identity comparison must
// still report EVENT_ID_REUSE.
func bigVariant(t *testing.T) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["note_big"] = 9007199254740993
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
