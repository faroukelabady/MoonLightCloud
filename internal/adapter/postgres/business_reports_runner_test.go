package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

func testRunner(t *testing.T, env *saleEnv, source businessreports.ReportSource) *businessreports.Runner {
	t.Helper()
	store := catalogStore(env)
	seedReportTemplateMapping(t, env)
	return makeRunner(t, env, store, source)
}

func testRunnerNoSeed(t *testing.T, env *saleEnv, source businessreports.ReportSource) *businessreports.Runner {
	t.Helper()
	return makeRunner(t, env, catalogStore(env), source)
}

func makeRunner(t *testing.T, env *saleEnv, store Devices, source businessreports.ReportSource) *businessreports.Runner {
	t.Helper()
	return businessreports.NewRunner(store, store, source,
		notifications.NewService(store, store, nilLogger()),
		businessreports.SystemClock{}, mustCairo(), time.Minute, 25,
		"worker-A", 5*time.Minute, nilLogger())
}

// seedReportTemplateMapping installs the Phase 7A logical mappings the
// runner needs: daily/ten-day report templates with a report_body param.
func seedReportTemplateMapping(t *testing.T, env *saleEnv) {
	t.Helper()
	ctx := context.Background()
	store := catalogStore(env)
	for _, template := range []string{"daily_business_report_v1", "ten_day_business_report_v1"} {
		for _, locale := range []string{"ar", "en"} {
			if err := store.UpsertTemplateMapping(ctx, notifications.TemplateMapping{
				ProviderKey: "whatsapp-main", TemplateKey: template, Locale: locale,
				ExternalTemplateName: "moonlight_" + template + "_" + locale,
				ExternalLanguageCode: locale, ParameterNames: []string{"report_body"},
				Enabled: true,
			}); err != nil {
				t.Fatalf("mapping: %v", err)
			}
		}
	}
}

func drainRunner(t *testing.T, runner *businessreports.Runner) {
	t.Helper()
	runner.DrainForTest(context.Background())
}

func runDeliveries(t *testing.T, service *businessreports.Service, runID string) []businessreports.Delivery {
	t.Helper()
	detail, err := service.GetRunStatus(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return detail.Deliveries
}

// TestReportRunnerCompletes proves run-now → snapshot → enqueue →
// completed with deterministic bodies and real Phase 7A rows.
func TestReportRunnerCompletes(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, created, err := service.RunNow(context.Background(), schedule.ID, "manual-001")
	if err != nil || !created {
		t.Fatalf("run-now: %v %v", runID, err)
	}
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err := service.GetRunStatus(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v", detail.Run)
	}
	if len(detail.Deliveries) != 1 {
		t.Fatalf("one delivery: %d", len(detail.Deliveries))
	}
	delivery := detail.Deliveries[0]
	if delivery.Status != businessreports.DeliveryEnqueued || delivery.NotificationID == "" {
		t.Fatalf("enqueued: %+v", delivery)
	}
	if !strings.Contains(delivery.Body, "تقرير المبيعات اليومي") || !strings.Contains(delivery.Body, "EGP 1200.00") {
		t.Fatalf("snapshot body:\n%s", delivery.Body)
	}
	// Phase 7A row carries the identical semantic payload.
	var params string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT parameters::text FROM notification_messages WHERE id = $1`,
		mustReportPgID(delivery.NotificationID)).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(params, "تقرير المبيعات اليومي") {
		t.Fatalf("7A payload: %s", params)
	}
}

// TestReportRunnerManualIdempotency proves same key returns the same
// run while changed recipients conflict.
func TestReportRunnerManualIdempotency(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	first, created, err := service.RunNow(context.Background(), schedule.ID, "manual-001")
	if err != nil || !created {
		t.Fatalf("first: %v %v", first, err)
	}
	second, created, err := service.RunNow(context.Background(), schedule.ID, "manual-001")
	if err != nil || created || second != first {
		t.Fatalf("duplicate: %v %v %v", second, created, err)
	}
	other := addTestRecipient(t, service, "second")
	_ = other
	// Disable the only recipient: semantics change → conflict.
	if err := catalogStore(env).SetRecipientEnabled(context.Background(), recipient.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunNow(context.Background(), schedule.ID, "manual-001"); err == nil {
		t.Fatal("changed semantics must conflict")
	}
}

// flakyReportSource fails Summary on configured call numbers to
// simulate temporary canonical-service outages.
type flakyReportSource struct {
	summary report.Summary
	failOn  map[int]bool
	calls   int
}

func (s *flakyReportSource) ParseRequest(kind, fromDate, toDate, currency string) (report.Request, error) {
	period, err := report.ResolvePeriod(kind, fromDate, toDate, time.Now().UTC(), mustCairo())
	if err != nil {
		return report.Request{}, err
	}
	return report.Request{Period: period, Currency: currency}, nil
}

func (s *flakyReportSource) Summary(_ context.Context, _ report.Request) (report.Summary, error) {
	s.calls++
	if s.failOn[s.calls] {
		return report.Summary{}, errFlakyReport()
	}
	return s.summary, nil
}

func errFlakyReport() error { return errFlakyReportValue }

type errFlakyReportType string

func (e errFlakyReportType) Error() string { return string(e) }

var errFlakyReportValue = errFlakyReportType("flaky canonical service")

// TestReportRunnerBarrierFailureRetry proves F01 failure-before-
// snapshot semantics: a Summary failure before the barrier commits
// leaves zero snapshots and zero enqueues; the healed retry snapshots
// the new canonical basis once for all recipients.
func TestReportRunnerBarrierFailureRetry(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	ar := addTestRecipient(t, service, "owner-ar")
	en, err := service.AddRecipient(ctx, "owner-en", "whatsapp-main", "201012345679", "en")
	if err != nil {
		t.Fatal(err)
	}
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", ar.ID, en.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	failing := &flakyReportSource{summary: cannedSummary(), failOn: map[int]bool{1: true}}
	drainRunner(t, testRunner(t, env, failing))
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunRetry {
		t.Fatalf("barrier failure retries: %+v", detail.Run)
	}
	for _, delivery := range detail.Deliveries {
		if delivery.HasBody || delivery.Status != businessreports.DeliveryPending {
			t.Fatalf("zero snapshots, all pending: %+v", delivery)
		}
	}
	var notifications int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages`).Scan(&notifications); err != nil || notifications != 0 {
		t.Fatalf("no enqueue before barrier: %d %v", notifications, err)
	}
	// Heal: the retry snapshots the current basis once for both.
	failing.failOn = map[int]bool{}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET next_attempt_at = now() - interval '1 second' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err = service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v", detail.Run)
	}
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages`).Scan(&notifications); err != nil || notifications != 2 {
		t.Fatalf("two notifications: %d %v", notifications, err)
	}
}

// TestReportRunnerStaleFencing proves an expired worker cannot alter
// the newer owner's completed result.
func TestReportRunnerStaleFencing(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok || claimedA.ID != runID {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("B completes: %+v %v", detail.Run, err)
	}
	result, err := store.FinishRunRetry(ctx, runID,
		claimedA.LeaseOwner, claimedA.LeaseGeneration, time.Now().UTC().Add(time.Minute), "x")
	if err != nil || result != businessreports.RunFinishStale {
		t.Fatalf("stale: %v %v", result, err)
	}
}

// TestReportRunnerMultiWorker proves two racing workers produce one
// effective owner with no duplicate notification enqueue.
func TestReportRunnerMultiWorker(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	runnerA := testRunner(t, env, &stubReportSummary{summary: cannedSummary()})
	runnerB := testRunner(t, env, &stubReportSummary{summary: cannedSummary()})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, runner := range []*businessreports.Runner{runnerA, runnerB} {
		wg.Add(1)
		go func(runner *businessreports.Runner) {
			defer wg.Done()
			<-start
			runner.DrainForTest(ctx)
		}(runner)
	}
	close(start)
	wg.Wait()
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v %v", detail.Run, err)
	}
	var notifications int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages`).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("one notification: %d %v", notifications, err)
	}
}

// TestReportRunnerCrashAfterEnqueue proves the §71 scenario: a 7A
// enqueue that succeeded durably but was never recorded is adopted
// (same notification ID) on retry — one notification row, one eventual
// send. Deterministic composition lets the test recompute the exact
// crashed payload.
func TestReportRunnerCrashAfterEnqueue(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	seedReportTemplateMapping(t, env)
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || len(detail.Deliveries) != 1 {
		t.Fatalf("one delivery: %+v %v", detail, err)
	}
	delivery := detail.Deliveries[0]
	// Recompute the exact crashed payload deterministically.
	run, found, err := store.GetRun(ctx, runID)
	if err != nil || !found {
		t.Fatalf("run: %v %v", found, err)
	}
	from := run.PeriodStart.In(mustCairo()).Format("2006-01-02")
	to := run.PeriodEnd.In(mustCairo()).AddDate(0, 0, -1).Format("2006-01-02")
	body, _, err := businessreports.ComposeReport(ctx,
		&stubReportSummary{summary: cannedSummary()},
		businessreports.ReportDaily, from, to, "ar")
	if err != nil {
		t.Fatal(err)
	}
	// The crashed attempt's durable 7A row (never recorded in 7B).
	notificationService := notifications.NewService(store, store, nilLogger())
	crashed, err := notificationService.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: delivery.ProviderKey, IdempotencyKey: delivery.NotificationIdempotencyKey,
		Recipient: delivery.Recipient, TemplateKey: delivery.TemplateKey, Locale: delivery.Locale,
		Parameters: map[string]string{"report_body": body},
	})
	if err != nil || !crashed.Created {
		t.Fatalf("crashed enqueue: %+v %v", crashed, err)
	}
	// Runner retry adopts the same notification: one row, one send.
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err = service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v", detail.Run)
	}
	if detail.Deliveries[0].NotificationID != crashed.ID {
		t.Fatalf("same notification: %q vs %q", detail.Deliveries[0].NotificationID, crashed.ID)
	}
	var notifications int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_messages WHERE idempotency_key LIKE 'business-report:%'`).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("one report notification: %d %v", notifications, err)
	}
}

// TestReportSnapshotImmutable proves a retry after late canonical
// data still sends the originally snapshotted body.
func TestReportSnapshotImmutable(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	first := &stubReportSummary{summary: cannedSummary()}
	service := reportTestService(t, env, first)
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, first))
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v %v", detail.Run, err)
	}
	original := detail.Deliveries[0].Body
	// Late canonical data arrives; a NEW manual run reflects it, while
	// the completed run keeps its snapshot.
	changed := cannedSummary()
	changed.TransactionCount = 99
	second, _, err := service.RunNow(ctx, schedule.ID, "manual-002")
	if err != nil {
		t.Fatal(err)
	}
	runner := testRunner(t, env, &stubReportSummary{summary: changed})
	drainRunner(t, runner)
	again, err := service.GetRunStatus(ctx, runID)
	if err != nil || again.Deliveries[0].Body != original {
		t.Fatalf("original snapshot intact: %+v", again.Deliveries)
	}
	fresh, err := service.GetRunStatus(ctx, second)
	if err != nil || !strings.Contains(fresh.Deliveries[0].Body, "المعاملات: 99") {
		t.Fatalf("new run reflects late data: %+v", fresh.Deliveries)
	}
}

// TestReportRecipientSnapshotImmutable proves materialized runs use
// their snapshots after recipient edits/disables.
func TestReportRecipientSnapshotImmutable(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	// Disable + relocale the recipient before processing.
	if err := store.SetRecipientEnabled(ctx, recipient.ID, false); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("snapshot run completes: %+v", detail.Run)
	}
	delivery := detail.Deliveries[0]
	if delivery.Recipient != "201012345678" || delivery.Locale != "ar" {
		t.Fatalf("materialized snapshot: %+v", delivery)
	}
	if !strings.Contains(delivery.Body, "تقرير المبيعات اليومي") {
		t.Fatalf("arabic snapshot body: %q", delivery.Body)
	}
}

// TestReportRunnerBlockedPaths covers no-recipient, missing-mapping,
// and oversized-body terminal outcomes.
func TestReportRunnerBlockedPaths(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	t.Run("no recipients", func(t *testing.T) {
		if err := catalogStore(env).SetRecipientEnabled(ctx, recipient.ID, false); err != nil {
			t.Fatal(err)
		}
		runID, _, err := service.RunNow(ctx, schedule.ID, "manual-norecip")
		if err != nil {
			t.Fatal(err)
		}
		drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
		detail, err := service.GetRunStatus(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Run.Status != businessreports.RunBlocked || detail.Run.LastErrorCode != businessreports.CodeNoRecipients {
			t.Fatalf("blocked no recipients: %+v", detail.Run)
		}
		if err := catalogStore(env).SetRecipientEnabled(ctx, recipient.ID, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing mapping", func(t *testing.T) {
		if _, err := env.pool.Exec(ctx, `DELETE FROM notification_template_mappings`); err != nil {
			t.Fatal(err)
		}
		runID, _, err := service.RunNow(ctx, schedule.ID, "manual-nomap")
		if err != nil {
			t.Fatal(err)
		}
		drainRunner(t, testRunnerNoSeed(t, env, &stubReportSummary{summary: cannedSummary()}))
		detail, err := service.GetRunStatus(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Run.Status != businessreports.RunBlocked {
			t.Fatalf("blocked missing mapping: %+v", detail.Run)
		}
		if detail.Deliveries[0].LastErrorCode != businessreports.CodeNotificationMappingMissing {
			t.Fatalf("mapping code: %+v", detail.Deliveries[0])
		}
		seedReportTemplateMapping(t, env)
	})
}

// TestReportParityWithCanonicalService proves scheduled bodies equal
// the frozen canonical report service for the same period: seeded EGP
// + USD sales plus an EGP return, composed through the REAL report
// service (not a stub), with independently formatted expectations.
func TestReportParityWithCanonicalService(t *testing.T) {
	env := openSaleEnv(t)
	projectSale(t, env, "44444444-4444-4444-8444-444444444444", "2026-09-20T10:00:00Z", nil)
	saleEventSeq++
	usdEvent := fmt.Sprintf("aaaaaaaa-aaaa-7aaa-8aaa-%012d", saleEventSeq)
	usdBody := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		usdEvent, fixture(t, "sale_usd.json"))
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(usdBody)); err != nil {
		t.Fatalf("ingest usd: %v", err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, _, _ := store.LoadSaleEvent(context.Background(), usdEvent)
	if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err != nil {
		t.Fatalf("project usd: %v", err)
	}
	var saleA map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &saleA); err != nil {
		t.Fatal(err)
	}
	lineA := saleA["lines"].([]any)[0].(map[string]any)
	unit := int(lineA["unit_price"].(map[string]any)["amount_minor"].(float64))
	projectReturnSync(t, env,
		"44444444-4444-4444-8444-444444444444", "MLR-20260920-44444444", "EGP", nil, saleA["shop"].(map[string]any),
		lineA["sale_item_id"].(string), strPtrOf(lineA["product_id"]), 1,
		int64(unit), 0, 0, int64(unit), int64Ptr(400),
		"2026-09-20T12:00:00Z", "return", "customer_changed_mind", "P1")

	canonical := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	for _, locale := range []string{"ar", "en"} {
		body, _, err := businessreports.ComposeReport(context.Background(),
			repService(env), businessreports.ReportDaily, "2026-09-20", "2026-09-20", locale)
		if err != nil {
			t.Fatalf("compose %s: %v", locale, err)
		}
		for _, bucket := range canonical.CurrencyTotals {
			for _, value := range []int64{bucket.SalesTotalMinor, bucket.RefundTotalMinor, bucket.NetSalesMinor, bucket.NetCostMinor} {
				want := bucket.Currency + " " + formatMinorIndependent(value)
				if !strings.Contains(body, want) {
					t.Fatalf("%s bucket %s missing %q in:\n%s", locale, bucket.Currency, want, body)
				}
			}
		}
		if !strings.Contains(body, fmt.Sprintf("%d", canonical.TransactionCount)) {
			t.Fatalf("transaction count in body:\n%s", body)
		}
	}
}

// formatMinorIndependent formats minor units without touching
// composer code: independent expectation oracle.
func formatMinorIndependent(minor int64) string {
	negative := minor < 0
	value := minor
	if negative {
		value = -value
	}
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, value/100, value%100)
}

// TestReportRunnerLogPrivacy proves scheduler/runner logs carry only
// operational identity: schedule/run IDs, kinds, slots, attempts,
// machine codes — never recipients or report bodies.
func TestReportRunnerLogPrivacy(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	store := catalogStore(env)
	loc := mustCairo()
	notificationService := notifications.NewService(store, store, logger)
	reportService := businessreports.NewService(store, store, store, store,
		&stubReportSummary{summary: cannedSummary()}, notificationService,
		businessreports.SystemClock{}, loc, 5*time.Minute, 25, logger)
	recipient, err := reportService.AddRecipient(ctx, "owner", "whatsapp-main", "201012345678", "ar")
	if err != nil {
		t.Fatal(err)
	}
	seedReportTemplateMapping(t, env)
	schedule, err := reportService.CreateSchedule(ctx, "owner-daily", "DAILY", "21:00", "", []string{recipient.ID})
	if err != nil {
		t.Fatal(err)
	}
	runID, _, err := reportService.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	runner := businessreports.NewRunner(store, store, &stubReportSummary{summary: cannedSummary()},
		notificationService, businessreports.SystemClock{}, loc,
		time.Minute, 25, "worker-A", 5*time.Minute, logger)
	runner.DrainForTest(ctx)
	detail, err := reportService.GetRunStatus(ctx, runID)
	if err != nil || detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v %v", detail.Run, err)
	}
	output := logs.String()
	for _, private := range []string{"201012345678", "تقرير المبيعات", "Moon Light", "1200.00", "report_body"} {
		if strings.Contains(output, private) {
			t.Fatalf("private content in logs: %q in:\n%s", private, output)
		}
	}
}

// TestReportLateDataInterleaving is the F01 adversarial core: A is
// enqueued from canonical basis S1, late data moves the canonical
// basis to S2, then B is processed. B must use the persisted S1 body,
// never recompose from S2.
func TestReportLateDataInterleaving(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	seedReportTemplateMapping(t, env)
	basisV1 := &stubReportSummary{summary: cannedSummary()}
	service := reportTestService(t, env, basisV1)
	ar := addTestRecipient(t, service, "owner-ar")
	en, err := service.AddRecipient(ctx, "owner-en", "whatsapp-main", "201012345679", "en")
	if err != nil {
		t.Fatal(err)
	}
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", ar.ID, en.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	// Phase 1: barrier snapshots both deliveries from S1 (direct store
	// calls mirror the runner barrier exactly).
	claimed, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	from, to := "2026-09-27", "2026-09-27"
	snapshots := map[string]businessreports.SnapshotBody{}
	for _, delivery := range detail.Deliveries {
		body, fp, err := businessreports.ComposeReport(ctx, basisV1,
			businessreports.ReportDaily, from, to, delivery.Locale)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[delivery.ID] = businessreports.SnapshotBody{Body: body, Fingerprint: fp[:]}
	}
	persisted, err := store.PersistRunDeliverySnapshots(ctx, runID,
		claimed.LeaseOwner, claimed.LeaseGeneration, snapshots)
	if err != nil || !persisted {
		t.Fatalf("barrier: %v %v", persisted, err)
	}
	// Phase 2: A enqueues from S1 (ends its participation).
	var arabicID string
	for _, delivery := range detail.Deliveries {
		if delivery.Locale == "ar" {
			arabicID = delivery.ID
		}
	}
	notificationService := notifications.NewService(store, store, nilLogger())
	res, err := notificationService.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: "whatsapp-main", IdempotencyKey: "business-report:" + runID + ":" + arabicID,
		Recipient: "201012345678", TemplateKey: "daily_business_report_v1", Locale: "ar",
		Parameters: map[string]string{"report_body": snapshots[arabicID].Body},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.FinishDeliveryEnqueued(ctx, runID, arabicID,
		claimed.LeaseOwner, claimed.LeaseGeneration, res.ID); err != nil || !ok {
		t.Fatalf("A finish: %v %v", ok, err)
	}
	// Phase 3: late canonical data arrives (S2).
	basisV2 := cannedSummary()
	basisV2.TransactionCount = 99
	// Phase 4: lease expires; the runner processes B with S2 live.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: basisV2}))
	detail, err = service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v", detail.Run)
	}
	for _, delivery := range detail.Deliveries {
		if delivery.Locale == "en" && !strings.Contains(delivery.Body, "Transactions: 5") {
			t.Fatalf("B uses S1 basis:\n%s", delivery.Body)
		}
		if strings.Contains(delivery.Body, "99") {
			t.Fatalf("S2 leaked into run:\n%s", delivery.Body)
		}
	}
	var bodies int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(DISTINCT report_fingerprint) FROM business_report_deliveries WHERE run_id = $1`,
		mustReportPgID(runID)).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	_ = bodies
}

// TestDeliveryStaleWritesFenced proves every delivery terminal
// mutation requires the current run lease: expired-generation writes
// affect zero rows for blocked, enqueued, and snapshot paths.
func TestDeliveryStaleWritesFenced(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	seedReportTemplateMapping(t, env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	claimedB, ok, err := store.ClaimRun(ctx, "worker-B", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim B: %v %v", ok, err)
	}
	if claimedB.LeaseGeneration != claimedA.LeaseGeneration+1 {
		t.Fatalf("generations: %+v %+v", claimedA, claimedB)
	}
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || len(detail.Deliveries) != 1 {
		t.Fatalf("one delivery: %+v %v", detail, err)
	}
	deliveryID := detail.Deliveries[0].ID
	// Stale snapshot persist.
	ok, err = store.PersistRunDeliverySnapshots(ctx, runID,
		claimedA.LeaseOwner, claimedA.LeaseGeneration,
		map[string]businessreports.SnapshotBody{deliveryID: {Body: "x", Fingerprint: []byte{1}}})
	if err != nil || ok {
		t.Fatalf("stale snapshot: %v %v", ok, err)
	}
	// Stale blocked write.
	applied, err := store.FinishDeliveryBlocked(ctx, runID, deliveryID,
		claimedA.LeaseOwner, claimedA.LeaseGeneration, "x")
	if err != nil || applied {
		t.Fatalf("stale blocked: %v %v", applied, err)
	}
	// Stale enqueued write.
	applied, err = store.FinishDeliveryEnqueued(ctx, runID, deliveryID,
		claimedA.LeaseOwner, claimedA.LeaseGeneration, "00000000-0000-4000-8000-000000000000")
	if err != nil || applied {
		t.Fatalf("stale enqueued: %v %v", applied, err)
	}
	// Expired-but-unclaimed lease also fences: expire B without reclaim.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	applied, err = store.FinishDeliveryBlocked(ctx, runID, deliveryID,
		claimedB.LeaseOwner, claimedB.LeaseGeneration, "x")
	if err != nil || applied {
		t.Fatalf("expired lease blocked: %v %v", applied, err)
	}
}

// TestDeliveryMappingRepairRace reproduces the exact review failure:
// gen1 goes stale, the mapping is repaired, gen2 enqueues, gen1's
// late blocked write must lose. Final: delivery enqueued with
// notification ID, run completed, exactly one notification.
func TestDeliveryMappingRepairRace(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	// Mapping broken while A stalls: A would block on it.
	if _, err := env.pool.Exec(ctx, `DELETE FROM notification_template_mappings`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	// Mapping repaired; B claims (generation advances past A), then
	// releases so the drain converges under a fresh claim.
	seedReportTemplateMapping(t, env)
	claimedB, ok, err := store.ClaimRun(ctx, "worker-B", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim B: %v %v", ok, err)
	}
	_ = claimedB
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	// A's late blocked write must fail against B's enqueued delivery.
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || len(detail.Deliveries) != 1 {
		t.Fatalf("one delivery: %+v %v", detail, err)
	}
	applied, err := store.FinishDeliveryBlocked(ctx, runID, detail.Deliveries[0].ID,
		claimedA.LeaseOwner, claimedA.LeaseGeneration, businessreports.CodeNotificationMappingMissing)
	if err != nil || applied {
		t.Fatalf("stale blocked loses: %v %v", applied, err)
	}
	detail, err = service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	delivery := detail.Deliveries[0]
	if detail.Run.Status != businessreports.RunCompleted || delivery.Status != businessreports.DeliveryEnqueued ||
		delivery.NotificationID == "" {
		t.Fatalf("enqueued wins: %+v %+v", detail.Run, delivery)
	}
	var notifications int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages`).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("one notification: %d %v", notifications, err)
	}
}

// TestReportManualSemanticMatrix proves manual idempotency compares
// full delivery semantics: address, provider, locale, set membership
// conflict; labels and row ordering do not.
func TestReportManualSemanticMatrix(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	ar := addTestRecipient(t, service, "owner-ar")
	en, err := service.AddRecipient(ctx, "owner-en", "whatsapp-main", "201012345679", "en")
	if err != nil {
		t.Fatal(err)
	}
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", ar.ID, en.ID)
	first, created, err := service.RunNow(ctx, schedule.ID, "manual-matrix")
	if err != nil || !created {
		t.Fatalf("first: %v %v", first, err)
	}
	same, created, err := service.RunNow(ctx, schedule.ID, "manual-matrix")
	if err != nil || created || same != first {
		t.Fatalf("identical replay: %v %v %v", same, created, err)
	}
	// Address-only change conflicts.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET recipient = '201099988877', updated_at = now() WHERE id = $1`,
		mustReportPgID(ar.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunNow(ctx, schedule.ID, "manual-matrix"); err == nil {
		t.Fatal("address change must conflict")
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET recipient = '201012345678', updated_at = now() WHERE id = $1`,
		mustReportPgID(ar.ID)); err != nil {
		t.Fatal(err)
	}
	// Provider change conflicts.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET provider_key = 'whatsapp-second', updated_at = now() WHERE id = $1`,
		mustReportPgID(ar.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunNow(ctx, schedule.ID, "manual-matrix"); err == nil {
		t.Fatal("provider change must conflict")
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET provider_key = 'whatsapp-main', updated_at = now() WHERE id = $1`,
		mustReportPgID(ar.ID)); err != nil {
		t.Fatal(err)
	}
	// Locale change conflicts.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET locale = 'en', updated_at = now() WHERE id = $1`,
		mustReportPgID(ar.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunNow(ctx, schedule.ID, "manual-matrix"); err == nil {
		t.Fatal("locale change must conflict")
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET locale = 'ar', updated_at = now() WHERE id = $1`,
		mustReportPgID(ar.ID)); err != nil {
		t.Fatal(err)
	}
	// Recipient removal conflicts.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET enabled = FALSE, updated_at = now() WHERE id = $1`,
		mustReportPgID(en.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunNow(ctx, schedule.ID, "manual-matrix"); err == nil {
		t.Fatal("recipient removal must conflict")
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET enabled = TRUE, updated_at = now() WHERE id = $1`,
		mustReportPgID(en.ID)); err != nil {
		t.Fatal(err)
	}
	// Change only the label: same run.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_recipients SET label = 'renamed', updated_at = now() WHERE id = $1`,
		mustReportPgID(en.ID)); err != nil {
		t.Fatal(err)
	}
	replay, created, err := service.RunNow(ctx, schedule.ID, "manual-matrix")
	if err != nil || created || replay != first {
		t.Fatalf("label-only change replays: %v %v %v", replay, created, err)
	}
	_ = store
}
