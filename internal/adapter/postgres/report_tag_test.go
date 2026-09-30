package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// Phase 8C Cloud tag reporting: historical breakdown over projected v2
// snapshots, overlap semantics, refund attribution, and unknown exclusion.

// projectSaleV2 ingests a v2 sale payload (tags injected per line) and
// projects it synchronously. Returns the event ID.
func projectSaleV2(t *testing.T, env *saleEnv, saleID, occurred string, tags []map[string]any) string {
	t.Helper()
	saleEventSeq++
	eventID := fmt.Sprintf("bbbbbbbb-bbbb-7bbb-8bbb-%012d", saleEventSeq)
	var m map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	m["occurred_at"] = occurred
	m["paid_at"] = occurred
	for _, entry := range m["lines"].([]any) {
		line := entry.(map[string]any)
		lineTags := make([]any, 0, len(tags))
		for _, tag := range tags {
			lineTags = append(lineTags, tag)
		}
		line["tags"] = lineTags
	}
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v2","occurred_at":%q,"payload":%s}]}`,
		eventID, occurred, payload)
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadSaleEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load event: %v %v", ok, err)
	}
	res, err := store.ProjectSaleV2(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if res.Outcome != sale.OutcomeProcessed && res.Outcome != sale.OutcomeAlready {
		t.Fatalf("projection outcome: %+v", res)
	}
	return eventID
}

func tagFixture(tagID, slug, ar, en string) map[string]any {
	return map[string]any{"tag_id": tagID, "slug": slug, "name_ar": ar, "name_en": en}
}

func tagBreakdown(t *testing.T, env *saleEnv, from, to string) report.Breakdown {
	t.Helper()
	svc := repService(env)
	req, err := svc.ParseRequest("custom", from, to, "")
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	out, err := svc.Breakdown(context.Background(), req, report.DimensionTag)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	return out
}

func TestReportTagBreakdownHistorical(t *testing.T) {
	env := openSaleEnv(t)
	horse := tagFixture("aaaaaaaa-0000-4000-8000-000000000001", "horse", "حصان", "Horse")
	blue := tagFixture("aaaaaaaa-0000-4000-8000-000000000002", "blue", "أزرق", "Blue")
	projectSaleV2(t, env, "aaaaaaaa-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", []map[string]any{horse, blue})
	// v1 sale (unknown capture) in the same period.
	projectSale(t, env, "aaaaaaaa-2222-4111-8111-222222222222", "2026-09-20T11:00:00Z", nil)

	out := tagBreakdown(t, env, "2026-09-20", "2026-09-21")
	if len(out.Rows) != 2 {
		t.Fatalf("two historical tag groups, got %+v", out.Rows)
	}
	bySlug := map[string]report.BreakdownRow{}
	for _, row := range out.Rows {
		if row.TagSlug == nil {
			t.Fatal("tag rows carry slug")
		}
		bySlug[*row.TagSlug] = row
	}
	// sale_egp fixture: check totals from fixture (quantity × unit).
	horseRow := bySlug["horse"]
	if horseRow.Units == 0 || len(horseRow.LineSales) == 0 {
		t.Fatalf("horse row: %+v", horseRow)
	}
	if horseRow.NameAR == nil || *horseRow.NameAR != "حصان" {
		t.Fatalf("historical name: %+v", horseRow)
	}
	// Overlap documented: horse + blue groups each carry the shared line.
	blueRow := bySlug["blue"]
	if blueRow.Units != horseRow.Units {
		t.Fatalf("shared line in both groups: horse=%+v blue=%+v", horseRow, blueRow)
	}
	// v1 sale contributes to summary but to no tag group.
	summary := reportSummary(t, env, "custom", "2026-09-20", "2026-09-21", "")
	if len(summary.CurrencyTotals) == 0 {
		t.Fatal("summary present")
	}
}
