package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/salesreport"
	"github.com/faroukelabady/MoonLightCloud/internal/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This read-only, opt-in proof reads a disposable Cloud populated by Retail's
// TestE2E_ReviewDemoSync. It does not create or delete databases or change rows.
func TestReviewSeedCloudReports(t *testing.T) {
	if os.Getenv("MOONLIGHT_REVIEW_SEED_E2E") != "1" {
		t.Skip("fresh-demo proof disabled")
	}
	ctx := context.Background()
	target, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || (target.Hostname() != "127.0.0.1" && target.Hostname() != "localhost") || !strings.HasPrefix(target.Path, "/moonlight_test_") || os.Getenv("ENVIRONMENT") != "development" {
		t.Fatal("fresh-demo proof requires an explicit loopback moonlight_test_* development database")
	}
	raw, err := os.ReadFile(os.Getenv("MOONLIGHT_REVIEW_SEED_EXPECT"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		StoreID string            `json:"store_id"`
		Sales   []json.RawMessage `json:"sales"`
		Lines   []json.RawMessage `json:"lines"`
		Refunds []json.RawMessage `json:"refunds"`
	}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected.Sales) != 39 || len(expected.Refunds) == 0 {
		t.Fatal("missing demo sale/refund evidence")
	}
	cfg, err := pgxpool.ParseConfig(target.String())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var sales, lines, refunds int
		err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM sales_projection),(SELECT count(*) FROM sale_lines_projection),(SELECT count(*) FROM return_refund_projection)`).Scan(&sales, &lines, &refunds)
		if err != nil {
			t.Fatal(err)
		}
		if sales == len(expected.Sales) && lines == len(expected.Lines) && refunds == len(expected.Refunds) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection incomplete: sales=%d lines=%d refunds=%d", sales, lines, refunds)
		}
		time.Sleep(100 * time.Millisecond)
	}
	repo := NewReporting(pool, 10*time.Second)
	periods := report.NewService(repo, clock.Fixed{T: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}, time.UTC)
	req, err := periods.ParseRequest("custom", "2026-10-01", "2026-11-11", "")
	if err != nil {
		t.Fatal(err)
	}
	req.Store, err = report.ParseStoreScope(expected.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	business := settings.NewService(NewBusinessSettings(pool, 10*time.Second), uuid.NewString, time.Now)
	svc := salesreport.NewService(repo, business)
	views := map[string]any{}
	for _, display := range []string{"all", "EGP", "USD"} {
		view, err := svc.Sales(ctx, req, display, salesreport.Filter{}, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if view.SalesTotalCount != len(expected.Sales) || len(view.Sales) != len(expected.Sales) {
			t.Fatalf("%s report omitted sales", display)
		}
		page, err := svc.Sales(ctx, req, display, salesreport.Filter{}, 10, 10)
		if err != nil {
			t.Fatal(err)
		}
		if page.SalesTotalCount != len(expected.Sales) || !reflect.DeepEqual(page.Summary, view.Summary) || !reflect.DeepEqual(page.Sales, view.Sales[10:20]) {
			t.Fatalf("%s pagination changed the report facts", display)
		}
		views[display] = view
		facts, err := repo.SalesReportFacts(ctx, expected.StoreID, req.Period.StartUTC, req.Period.EndUTC, req.GeneratedAt(), true)
		if err != nil {
			t.Fatal(err)
		}
		rawTax, err := salesreport.Build(facts, salesreport.Options{Display: display, Loc: time.UTC, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		views[display+"_tax_on"] = rawTax
	}
	other := req
	other.Store, err = report.ParseStoreScope(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := svc.Sales(ctx, other, "EGP", salesreport.Filter{}, 0, 100)
	if err != nil || empty.SalesTotalCount != 0 {
		t.Fatal("Store-scoped report exposed another Store's sales", err)
	}
	read := func(query string) json.RawMessage {
		var raw []byte
		if err := pool.QueryRow(ctx, query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	result := map[string]any{
		"store_id": expected.StoreID, "reports": views, "pagination": "passed in all/EGP/USD", "other_store_empty": true,
		"images":       read(`SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY m.sha256),'[]') FROM (SELECT sha256 FROM media_blobs) m`),
		"sales":        read(`SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]') FROM (SELECT sale_id AS id,sale_number,currency,subtotal_minor AS subtotal_cents,discount_minor AS discount_cents,tax_minor AS tax_cents,total_minor AS total_cents,fx_rate_microrate AS fx_microrate FROM sales_projection) s`),
		"lines":        read(`SELECT COALESCE(jsonb_agg(to_jsonb(l) ORDER BY l.id),'[]') FROM (SELECT l.sale_item_id AS id,l.sale_id,l.sku,l.quantity,s.currency,l.unit_price_minor AS unit_price_cents,l.line_total_minor AS line_total_cents,l.cost_minor AS cost_cents,l.variant_attributes AS attributes FROM sale_lines_projection l JOIN sales_projection s USING(sale_id)) l`),
		"refunds":      read(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]') FROM (SELECT return_refund_id AS id,sale_id,kind,currency,refund_total_minor AS refund_total_cents FROM return_refund_projection) r`),
		"refund_lines": read(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.correction_id,r.sale_item_id),'[]') FROM (SELECT return_refund_id AS correction_id,original_sale_line_id AS sale_item_id,quantity,restocked,gross_minor AS gross_cents,discount_minor AS discount_cents,tax_minor AS tax_cents,refund_minor AS refund_cents,cost_minor AS cost_cents FROM return_refund_lines_projection) r`),
		"processing":   read(`SELECT COALESCE(jsonb_agg(to_jsonb(p)),'[]') FROM (SELECT processor,status,count(*) AS count FROM sync_event_processing GROUP BY processor,status ORDER BY processor,status) p`),
	}
	cloudURL := os.Getenv("MOONLIGHT_REVIEW_CLOUD_URL")
	u, err := url.Parse(cloudURL)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("report HTTP proof requires loopback Cloud")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(cloudURL + "/api/v1/reports/sales/summary?period=custom&from_date=2026-10-01&to_date=2026-11-11&store_id=" + expected.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("summary HTTP status: %d", response.StatusCode)
	}
	var summary json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	result["http_summary"] = summary
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("MOONLIGHT_REVIEW_CLOUD_REPORTS"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Read-only Cloud report proof: 39 sales, all refunds projected; all/EGP/USD detail pages and Store isolation passed")
}
