package sale

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// v2 validation: every v1 invariant inherited plus tag rules. v1 code
// and fixtures are untouched.

func loadV2Base(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/sale_usd.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	lines := m["lines"].([]any)
	for _, entry := range lines {
		entry.(map[string]any)["tags"] = []any{
			map[string]any{
				"tag_id": "aaaaaaaa-0000-4000-8000-000000000001",
				"slug":   "horse", "name_ar": "حصان", "name_en": "Horse",
			},
		}
	}
	return m
}

func decodeV2Map(t *testing.T, m map[string]any) (PayloadV2, error) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return DecodeV2(raw)
}

func TestDecodeV2Valid(t *testing.T) {
	p, err := decodeV2Map(t, loadV2Base(t))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	valid, err := ValidateV2(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(valid.Lines) != 1 || len(valid.Lines[0].Tags) != 1 {
		t.Fatal("one line with one tag")
	}
	if valid.Lines[0].Tags[0].Slug != "horse" {
		t.Fatal("tag verbatim")
	}
}

func TestDecodeV2EmptyTagsAllowed(t *testing.T) {
	m := loadV2Base(t)
	m["lines"].([]any)[0].(map[string]any)["tags"] = []any{}
	p, err := decodeV2Map(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateV2(p); err != nil {
		t.Fatalf("captured empty validates: %v", err)
	}
}

func TestDecodeV2RequiresTagsArrayOnEveryLine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   func(map[string]any)
		valid bool
	}{
		{"missing", func(line map[string]any) { delete(line, "tags") }, false},
		{"null", func(line map[string]any) { line["tags"] = nil }, false},
		{"empty", func(line map[string]any) { line["tags"] = []any{} }, true},
		{"populated", func(map[string]any) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := loadV2Base(t)
			tc.set(m["lines"].([]any)[0].(map[string]any))
			p, err := decodeV2Map(t, m)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ValidateV2(p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	// A valid line must not hide a malformed line in the same event.
	m := loadV2Base(t)
	lines := m["lines"].([]any)
	copyLine := map[string]any{}
	for k, v := range lines[0].(map[string]any) {
		copyLine[k] = v
	}
	copyLine["sale_item_id"] = "33333333-3333-4333-8333-333333333334"
	delete(copyLine, "tags")
	m["lines"] = append(lines, copyLine)
	for _, value := range m["totals"].(map[string]any) {
		money := value.(map[string]any)
		money["amount_minor"] = money["amount_minor"].(float64) * 2
	}
	for _, value := range m["payments"].([]any) {
		money := value.(map[string]any)["amount"].(map[string]any)
		money["amount_minor"] = money["amount_minor"].(float64) * 2
	}
	p, err := decodeV2Map(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateV2(p); err == nil || !strings.Contains(err.Error(), "lines[1].tags") {
		t.Fatalf("mixed valid and missing tags must reject at malformed line: %v", err)
	}
}

func TestDecodeV2TagSlugAndIdentityPolicy(t *testing.T) {
	baseID := "aaaaaaaa-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name, slug string
		valid      bool
	}{
		{"canonical", "blue-horse-7", true},
		{"space", "bad slug", false},
		{"slash", "bad/slug", false},
		{"control", "bad\nslug", false},
		{"non-ASCII", "حصان", false},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := loadV2Base(t)
			m["lines"].([]any)[0].(map[string]any)["tags"].([]any)[0].(map[string]any)["slug"] = tc.slug
			p, err := decodeV2Map(t, m)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ValidateV2(p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	for _, tc := range []struct {
		name, secondID string
		valid          bool
	}{
		{"exact duplicate", baseID, false},
		{"case variant duplicate", "AAAAAAAA-0000-4000-8000-000000000001", false},
		{"distinct identity same label", "aaaaaaaa-0000-4000-8000-000000000002", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := loadV2Base(t)
			line := m["lines"].([]any)[0].(map[string]any)
			second := map[string]any{"tag_id": tc.secondID, "slug": "horse", "name_ar": "حصان", "name_en": "Horse"}
			line["tags"] = append(line["tags"].([]any), second)
			p, err := decodeV2Map(t, m)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ValidateV2(p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestDecodeV2TagRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"bad tag uuid", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["tags"] = []any{
				map[string]any{"tag_id": "nope", "slug": "x", "name_ar": "x", "name_en": "x"}}
		}},
		{"duplicate tag", func(m map[string]any) {
			tag := map[string]any{"tag_id": "aaaaaaaa-0000-4000-8000-000000000001", "slug": "x", "name_ar": "x", "name_en": "x"}
			m["lines"].([]any)[0].(map[string]any)["tags"] = []any{tag, tag}
		}},
		{"empty slug", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["tags"] = []any{
				map[string]any{"tag_id": "aaaaaaaa-0000-4000-8000-000000000001", "slug": "", "name_ar": "x", "name_en": "x"}}
		}},
		{"empty names", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["tags"] = []any{
				map[string]any{"tag_id": "aaaaaaaa-0000-4000-8000-000000000001", "slug": "x", "name_ar": "", "name_en": "x"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := loadV2Base(t)
			tc.mutate(m)
			p, err := decodeV2Map(t, m)
			if err != nil {
				return // decode rejection also satisfies the rule
			}
			if _, err := ValidateV2(p); err == nil {
				t.Fatal("must reject")
			}
		})
	}
}

func TestDecodeV2InheritsV1MoneyRules(t *testing.T) {
	m := loadV2Base(t)
	m["lines"].([]any)[0].(map[string]any)["line_total"] = map[string]any{"amount_minor": 1, "currency": "USD"}
	p, err := decodeV2Map(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateV2(p); err == nil {
		t.Fatal("line_total mismatch must reject in v2")
	}
}

func TestDecodeV2Strict(t *testing.T) {
	if _, err := DecodeV2(json.RawMessage(`{"sale_id":1}`)); err == nil {
		t.Fatal("typed decode must reject")
	}
	if _, err := ValidateV2(PayloadV2{}); err == nil {
		t.Fatal("empty v2 must not validate")
	}
}
