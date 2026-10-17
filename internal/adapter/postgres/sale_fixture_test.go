package postgres

import (
	"encoding/json"
	"testing"
)

// taggedSaleFixture is the USD sale fixture with two tag snapshots on every
// line, as a sale.finalized.v3 payload without variant fields.
func taggedSaleFixture(t *testing.T, _ map[string]any) string {
	t.Helper()
	raw := fixture(t, "sale_usd.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	for _, entry := range m["lines"].([]any) {
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
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// withSaleTags returns payload with an empty tags array on every line that
// has none. Older inline fixtures predate tag snapshots; sale.finalized.v3
// requires the array (empty means "captured, no tags").
func withSaleTags(t *testing.T, payload string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return payload // malformed on purpose: let the caller's test see it
	}
	lines, ok := m["lines"].([]any)
	if !ok {
		return payload
	}
	changed := false
	for _, entry := range lines {
		if line, ok := entry.(map[string]any); ok {
			if _, has := line["tags"]; !has {
				line["tags"] = []any{}
				changed = true
			}
		}
	}
	if !changed {
		return payload
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
