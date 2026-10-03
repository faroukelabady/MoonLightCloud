package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/whatsapp"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

type r1SummarySource struct {
	businessreports.ReportSource
	calls atomic.Int32
}

func (s *r1SummarySource) Summary(ctx context.Context, r report.Request) (report.Summary, error) {
	s.calls.Add(1)
	return s.ReportSource.Summary(ctx, r)
}

// Both adapters are real; only their remote providers are TLS test servers.
func r1Providers(t *testing.T, fail string) (*notifications.Registry, *[]string, *[]string, *sync.Mutex) {
	t.Helper()
	registry := notifications.NewRegistry()
	var mu sync.Mutex
	tgBodies, waBodies := []string{}, []string{}
	tg := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		tgBodies = append(tgBodies, body.Text)
		count := len(tgBodies)
		mu.Unlock()
		if fail == "telegram-main" {
			w.WriteHeader(403)
			io.WriteString(w, `{"ok":false,"error_code":403,"description":"private raw provider description"}`)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":111}}}`, count)
	}))
	t.Cleanup(tg.Close)
	wa := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Template struct {
				Components []struct {
					Parameters []struct {
						Text string `json:"text"`
					} `json:"parameters"`
				} `json:"components"`
			} `json:"template"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		text := ""
		if len(body.Template.Components) > 0 && len(body.Template.Components[0].Parameters) > 0 {
			text = body.Template.Components[0].Parameters[0].Text
		}
		mu.Lock()
		waBodies = append(waBodies, text)
		count := len(waBodies)
		mu.Unlock()
		if fail == "whatsapp-main" {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":131030,"message":"private raw provider description"}}`)
			return
		}
		fmt.Fprintf(w, `{"messages":[{"id":"wamid.r1.%d"}]}`, count)
	}))
	t.Cleanup(wa.Close)
	tp := telegramTestProvider(t, &botStub{server: tg})
	wp, err := whatsapp.NewProvider(config.WhatsAppNotificationConfig{ProviderKey: "whatsapp-main", BaseURL: wa.URL, GraphVersion: "v25.0", PhoneNumberID: "123456789", AccessToken: "fake-wa-secret", HTTPTimeout: 5 * time.Second}, wa.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []notifications.NotificationProvider{tp, wp} {
		if err := registry.Register(p.Key(), p); err != nil {
			t.Fatal(err)
		}
	}
	return registry, &tgBodies, &waBodies, &mu
}

func TestTelegramR1ReportMatrix(t *testing.T) {
	for _, kind := range []string{"DAILY", "TEN_DAY"} {
		for _, locale := range []string{"ar", "en"} {
			for _, fail := range []string{"", "telegram-main", "whatsapp-main"} {
				t.Run(kind+"/"+locale+"/fail="+fail, func(t *testing.T) {
					env := openSaleEnv(t)
					ctx := context.Background()
					seedTelegramTemplateMapping(t, env)
					seedReportTemplateMapping(t, env)
					summary := cannedSummary()
					summary.CurrencyTotals[0].SalesTotalMinor = 9007199254740993
					summary.CurrencyTotals[0].NetSalesMinor = 9007199254740993
					source := &r1SummarySource{ReportSource: &stubReportSummary{summary: summary}}
					service := reportTestService(t, env, source)
					tg, err := service.AddRecipient(ctx, "TG", "telegram-main", "@"+strings.Repeat("a", 32), locale)
					if err != nil {
						t.Fatal(err)
					}
					wa, err := service.AddRecipient(ctx, "WA", "whatsapp-main", "201012345678", locale)
					if err != nil {
						t.Fatal(err)
					}
					anchor := ""
					if kind == "TEN_DAY" {
						anchor = "2000-01-01"
					}
					schedule := createTestSchedule(t, service, "matrix", kind, "21:00", anchor, tg.ID, wa.ID)
					run, _, err := service.RunNow(ctx, schedule.ID, "matrix-run")
					if err != nil {
						t.Fatal(err)
					}
					drainRunner(t, testRunnerNoSeed(t, env, source))
					deliveries := runDeliveries(t, service, run)
					if len(deliveries) != 2 || source.calls.Load() != 1 {
						t.Fatalf("one canonical basis: deliveries=%d calls=%d", len(deliveries), source.calls.Load())
					}
					if deliveries[0].Body != deliveries[1].Body || !strings.Contains(deliveries[0].Body, "EGP 90071992547409.93") || !strings.Contains(deliveries[0].Body, "USD 25.00") {
						t.Fatal("financial basis/money/currency mismatch")
					}
					registry, tgBodies, waBodies, mu := r1Providers(t, fail)
					notifications.NewDispatcher(catalogStore(env), registry, notifications.SystemClock{}, "matrix", nilLogger()).DrainForTest(ctx)
					for _, d := range deliveries {
						status, _ := r1State(t, env, d.NotificationID)
						expected := "accepted"
						if d.ProviderKey == fail {
							expected = "blocked"
						}
						if status != expected {
							t.Fatalf("provider %s=%s want=%s", d.ProviderKey, status, expected)
						}
					}
					mu.Lock()
					if len(*tgBodies) != 1 || len(*waBodies) != 1 || (*tgBodies)[0] != deliveries[0].Body || (*waBodies)[0] != deliveries[0].Body {
						mu.Unlock()
						t.Fatal("wire body differs from immutable snapshot")
					}
					mu.Unlock()
					source.ReportSource = &stubReportSummary{summary: cannedSummary()}
					drainRunner(t, testRunnerNoSeed(t, env, source))
					again := runDeliveries(t, service, run)
					if again[0].Body != deliveries[0].Body || source.calls.Load() != 1 {
						t.Fatal("completed snapshot recomputed")
					}
				})
			}
		}
	}
}

func r1OpsLifecycle(t *testing.T, env *saleEnv, recipient string) {
	t.Helper()
	ctx := context.Background()
	store := catalogStore(env)
	service := opsService(store)
	if _, err := store.CreateOpsRecipient(ctx, ids.System{}.New(), "TG ops", "telegram-main", recipient, "en", time.Now()); err != nil {
		t.Fatal(err)
	}
	processor := operations.NewAlertProcessor(store, service, newOpsNotifier(store), ids.System{}.New, time.Now, operations.NewMetrics())
	incident, _, err := service.OpenStateful(ctx, operations.RuleDeviceOffline, "device", env.devID, "")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := processor.BuildDeliveries(ctx, incident, operations.EventOpened)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.MaterializeOpen(ctx, incident.ID, opened); err != nil {
		t.Fatal(err)
	}
	resolved, err := processor.BuildDeliveries(ctx, incident, operations.EventResolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ResolveAtomic(ctx, incident.ID, operations.ResolutionReconnect, resolved, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		worked, err := processor.ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	var count int
	if err = env.pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages WHERE idempotency_key LIKE 'ops-alert:%'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("open/resolved intents=%d %v", count, err)
	}
}

func TestTelegramR1OperationalAlerts(t *testing.T) {
	env := openSaleEnv(t)
	seedTelegramTemplateMapping(t, env)
	recipient := "@" + strings.Repeat("a", 32)
	r1OpsLifecycle(t, env, recipient)
	registry, tgBodies, _, mu := r1Providers(t, "")
	notifications.NewDispatcher(catalogStore(env), registry, notifications.SystemClock{}, "ops", nilLogger()).DrainForTest(context.Background())
	rows, err := env.pool.Query(context.Background(), `SELECT d.recipient_snapshot,d.body_snapshot,n.dispatch_status,n.recipient FROM operational_alert_deliveries d JOIN notification_messages n ON n.id=d.notification_id ORDER BY d.created_at,d.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	bodies := []string{}
	for rows.Next() {
		var snapshot, body, status, address string
		if err = rows.Scan(&snapshot, &body, &status, &address); err != nil {
			t.Fatal(err)
		}
		if snapshot != recipient || address != recipient || status != "accepted" {
			t.Fatal("alert recipient/snapshot/dispatch mismatch")
		}
		bodies = append(bodies, body)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*tgBodies) != 2 || len(bodies) != 2 {
		t.Fatal("must dispatch opened and resolved")
	}
	for _, body := range bodies {
		found := false
		for _, sent := range *tgBodies {
			found = found || sent == body
		}
		if !found {
			t.Fatal("alert body changed at provider")
		}
	}
}

func r1DatabaseState(t *testing.T, env *saleEnv) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"notification_messages", "notification_template_mappings", "notification_delivery_status_history", "business_report_recipients", "business_report_schedules", "business_report_schedule_recipients", "business_report_runs", "business_report_deliveries", "operational_alert_recipients", "operational_incidents", "operational_alert_deliveries", "operational_recovery_actions", "sales_projection", "sale_lines_projection", "sale_payments_projection"} {
		var state string
		if err := env.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]')::text FROM `+table+` t`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		result[table] = state
	}
	return result
}

func TestTelegramR1V25To26Preservation(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	conn, err := sql.Open("pgx", env.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = migrate.DownTo(ctx, conn, 25); err != nil {
		t.Fatal(err)
	}
	seedTelegramTemplateMapping(t, env)
	id := r1Enqueue(t, env, "existing-identity", "-000111", "old content")
	source := &stubReportSummary{summary: cannedSummary()}
	service := reportTestService(t, env, source)
	recipient, err := service.AddRecipient(ctx, "old TG", "telegram-main", "@operations", "ar")
	if err != nil {
		t.Fatal(err)
	}
	schedule := createTestSchedule(t, service, "old schedule", "DAILY", "08:00", "", recipient.ID)
	if _, _, err = service.RunNow(ctx, schedule.ID, "old-run"); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunnerNoSeed(t, env, source))
	r1OpsLifecycle(t, env, "@operations")
	projectSale(t, env, "44444444-4444-4444-8444-444444444444", "2026-09-20T10:00:00Z", nil)
	r, err := env.pool.Exec(ctx, `INSERT INTO notification_delivery_status_history(notification_id,provider_key,provider_message_id,provider_status_raw,canonical_status,event_fingerprint) VALUES($1,'telegram-main','111:42','ACCEPTED','ACCEPTED','\x01')`, id)
	if err != nil || r.RowsAffected() != 1 {
		t.Fatal("history fixture", err)
	}
	before := r1DatabaseState(t, env)
	if err = migrate.UpTo(ctx, conn, 26); err != nil {
		t.Fatal(err)
	}
	version, err := migrate.Current(ctx, conn)
	if err != nil || version != 26 {
		t.Fatalf("version=%d %v", version, err)
	}
	after := r1DatabaseState(t, env)
	for table, state := range before {
		if state != after[table] {
			t.Fatalf("migration changed %s", table)
		}
	}
	// Explicit rollback is safe while all persisted values fit the old bound.
	if err = migrate.DownTo(ctx, conn, 25); err != nil {
		t.Fatal(err)
	}
	if err = migrate.UpTo(ctx, conn, 26); err != nil {
		t.Fatal(err)
	}
	maximum := "@" + strings.Repeat("a", 32)
	r1Enqueue(t, env, "wide-identity", maximum, "new content")
	preserved := r1DatabaseState(t, env)
	if err = migrate.DownTo(ctx, conn, 25); err == nil {
		t.Fatal("rollback must refuse wider durable values")
	}
	version, _ = migrate.Current(ctx, conn)
	if version != 26 {
		t.Fatal("failed rollback changed version")
	}
	after = r1DatabaseState(t, env)
	for table, state := range preserved {
		if state != after[table] {
			t.Fatal("failed rollback changed durable state", table)
		}
	}
	t.Log("exact v25→v26 preserved 15 seeded tables; safe short-value down/up; wide-value rollback refused without truncation")
}

func TestTelegramR1SaleSeededExactMoney(t *testing.T) {
	for _, kind := range []string{"DAILY", "TEN_DAY"} {
		t.Run(kind, func(t *testing.T) {
			env := openSaleEnv(t)
			ctx := context.Background()
			date := time.Now().In(mustCairo()).AddDate(0, 0, -1)
			occurred := time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, mustCairo()).UTC().Format(time.RFC3339)
			const exact = "9007199254740993"
			projectSale(t, env, "44444444-4444-4444-8444-444444444444", occurred, func(m map[string]any) {
				totals := m["totals"].(map[string]any)
				for _, field := range []string{"subtotal", "total"} {
					totals[field].(map[string]any)["amount_minor"] = json.Number(exact)
				}
				line := m["lines"].([]any)[0].(map[string]any)
				line["quantity"] = 1
				line["unit_price"].(map[string]any)["amount_minor"] = json.Number(exact)
				line["line_total"].(map[string]any)["amount_minor"] = json.Number(exact)
				m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = json.Number(exact)
			})
			var usd map[string]any
			if err := json.Unmarshal([]byte(fixture(t, "sale_usd.json")), &usd); err != nil {
				t.Fatal(err)
			}
			usd["occurred_at"], usd["paid_at"] = occurred, occurred
			raw, err := json.Marshal(usd)
			if err != nil {
				t.Fatal(err)
			}
			event := ids.System{}.New()
			batch := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":%q,"payload":%s}]}`, event, occurred, raw)
			result, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(batch))
			if err != nil || result.Events[0].Status != "accepted" {
				t.Fatal("USD ingest", err)
			}
			store := catalogStore(env)
			record, ok, err := store.LoadSaleEvent(ctx, event)
			if err != nil || !ok {
				t.Fatal(err)
			}
			if projected, err := store.ProjectSale(ctx, record, time.Now()); err != nil || projected.Outcome != 1 {
				t.Fatal("USD projection", err)
			}
			seedTelegramTemplateMapping(t, env)
			canonical := repService(env)
			source := &r1SummarySource{ReportSource: canonical}
			service := reportTestService(t, env, source)
			ar, err := service.AddRecipient(ctx, "Arabic", "telegram-main", "@operations", "ar")
			if err != nil {
				t.Fatal(err)
			}
			en, err := service.AddRecipient(ctx, "English", "telegram-main", "@operations_en", "en")
			if err != nil {
				t.Fatal(err)
			}
			anchor := ""
			if kind == "TEN_DAY" {
				anchor = "2000-01-01"
			}
			schedule := createTestSchedule(t, service, "sale-seeded", kind, "21:00", anchor, ar.ID, en.ID)
			run, _, err := service.RunNow(ctx, schedule.ID, "sale-seeded")
			if err != nil {
				t.Fatal(err)
			}
			drainRunner(t, testRunnerNoSeed(t, env, source))
			deliveries := runDeliveries(t, service, run)
			if len(deliveries) != 2 || source.calls.Load() != 1 {
				t.Fatal("financial basis not shared")
			}
			for _, d := range deliveries {
				if !strings.Contains(d.Body, "EGP 90071992547409.93") || !strings.Contains(d.Body, "USD 12.50") {
					t.Fatal("fresh exact money or currency separation wrong")
				}
			}
			registry, tgBodies, _, mu := r1Providers(t, "")
			notifications.NewDispatcher(store, registry, notifications.SystemClock{}, "sale-seeded", nilLogger()).DrainForTest(ctx)
			mu.Lock()
			defer mu.Unlock()
			if len(*tgBodies) != 2 {
				t.Fatal("expected two localized sends")
			}
			for _, d := range deliveries {
				found := false
				for _, body := range *tgBodies {
					found = found || body == d.Body
				}
				if !found {
					t.Fatal("financial body mutated in transit")
				}
			}
			t.Log("fresh public Sale ingestion/projection → canonical Summary once → ar/en snapshots → byte-identical Telegram text; exact EGP 90071992547409.93 and separate USD 12.50")
		})
	}
}
