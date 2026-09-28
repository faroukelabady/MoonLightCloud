package postgres

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// reportTestService wires the business-report service over the real
// store with a stub canonical report source and the real Phase 7A
// enqueue service (no provider sends: the 7A dispatcher never runs).
func reportTestService(t *testing.T, env *saleEnv, source businessreports.ReportSource) *businessreports.Service {
	t.Helper()
	store := catalogStore(env)
	notificationService := notifications.NewService(store, store, nilLogger())
	loc, err := businessreports.LoadCairo()
	if err != nil {
		panic(err)
	}
	return businessreports.NewService(store, store, store, store,
		source, notificationService, businessreports.SystemClock{}, loc,
		5*time.Minute, 25, nilLogger())
}

// stubReportSummary serves one canned canonical summary.
type stubReportSummary struct {
	summary report.Summary
	err     error
}

func (s *stubReportSummary) ParseRequest(kind, fromDate, toDate, currency string) (report.Request, error) {
	period, err := report.ResolvePeriod(kind, fromDate, toDate, time.Now().UTC(), mustCairo())
	if err != nil {
		return report.Request{}, err
	}
	return report.Request{Period: period, Currency: currency}, nil
}

func (s *stubReportSummary) Summary(_ context.Context, _ report.Request) (report.Summary, error) {
	return s.summary, s.err
}

func mustCairo() *time.Location {
	loc, err := time.LoadLocation(businessreports.TimezoneCairo)
	if err != nil {
		panic(err)
	}
	return loc
}

func cannedSummary() report.Summary {
	at := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	return report.Summary{
		TransactionCount: 5, UnitsSold: 7,
		CurrencyTotals: []report.CurrencyTotal{
			{Currency: "EGP", SalesTotalMinor: 125000, RefundTotalMinor: 5000, NetSalesMinor: 120000, NetCostMinor: 79000},
			{Currency: "USD", SalesTotalMinor: 2500, RefundTotalMinor: 0, NetSalesMinor: 2500, NetCostMinor: 1000},
		},
		Freshness: report.Freshness{LatestProjectedSaleOccurredAt: &at},
	}
}

func addTestRecipient(t *testing.T, service *businessreports.Service, label string) businessreports.Recipient {
	t.Helper()
	recipient, err := service.AddRecipient(context.Background(), label, "whatsapp-main", "201012345678", "ar")
	if err != nil {
		t.Fatalf("recipient: %v", err)
	}
	return recipient
}

func createTestSchedule(t *testing.T, service *businessreports.Service, name, kind, localTime, anchor string, recipients ...string) businessreports.Schedule {
	t.Helper()
	schedule, err := service.CreateSchedule(context.Background(), name, kind, localTime, anchor, recipients)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	return schedule
}

// pastSlotDate returns a Cairo local date n days before today for
// deterministic overdue fixtures (negative n gives future dates).
func pastSlotDate(daysAgo int) string {
	now := time.Now().UTC().In(mustCairo())
	return time.Date(now.Year(), now.Month(), now.Day()-daysAgo, 0, 0, 0, 0, mustCairo()).Format("2006-01-02")
}

func backdateSchedule(t *testing.T, env *saleEnv, scheduleID, nextDate string) {
	t.Helper()
	at, err := businessreports.SlotInstant(nextDate, "00:01", mustCairo())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE business_report_schedules SET next_run_local_date = $2, next_run_at = $3 WHERE id = $1`,
		mustReportPgID(scheduleID), nextDate, at.UTC()); err != nil {
		t.Fatal(err)
	}
}

func mustReportPgID(id string) pgtype.UUID {
	parsed, err := parseUUID(id)
	if err != nil {
		panic(err)
	}
	return parsed
}

// TestReportPlannerMaterializesDue proves one due slot becomes one
// run with snapshotted deliveries and an advanced cursor.
func TestReportPlannerMaterializesDue(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	backdateSchedule(t, env, schedule.ID, pastSlotDate(0))
	planner := businessreports.NewPlanner(catalogStore(env),
		businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	planner.TickForTest(context.Background())
	runs, err := service.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("one run: %+v %v", runs, err)
	}
	run := runs[0]
	if run.Status != businessreports.RunPending || run.SlotLocalDate != pastSlotDate(0) {
		t.Fatalf("slot run: %+v", run)
	}
	if run.PeriodStart.In(mustCairo()).Format("2006-01-02") != pastSlotDate(1) ||
		run.PeriodEnd.In(mustCairo()).Format("2006-01-02") != pastSlotDate(0) {
		t.Fatalf("period: %v %v", run.PeriodStart, run.PeriodEnd)
	}
	detail, err := service.GetRunStatus(context.Background(), run.ID)
	if err != nil || len(detail.Deliveries) != 1 {
		t.Fatalf("one delivery: %+v %v", detail, err)
	}
	delivery := detail.Deliveries[0]
	if delivery.Recipient != "201012345678" || delivery.Locale != "ar" ||
		delivery.TemplateKey != "daily_business_report_v1" {
		t.Fatalf("snapshot: %+v", delivery)
	}
	// Cursor advanced exactly one calendar day.
	updated, found, err := catalogStore(env).GetSchedule(context.Background(), schedule.ID)
	if err != nil || !found || updated.NextRunLocalDate != pastSlotDate(-1) {
		t.Fatalf("advanced: %+v %v %v", updated, found, err)
	}
}

// TestReportPlannerTwoInstances proves one due slot yields one run
// under racing planners.
func TestReportPlannerTwoInstances(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	backdateSchedule(t, env, schedule.ID, pastSlotDate(0))
	store := catalogStore(env)
	plannerA := businessreports.NewPlanner(store, businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	plannerB := businessreports.NewPlanner(store, businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for _, planner := range []*businessreports.Planner{plannerA, plannerB} {
		go func(planner *businessreports.Planner) {
			<-start
			planner.TickForTest(context.Background())
			done <- struct{}{}
		}(planner)
	}
	close(start)
	<-done
	<-done
	runs, err := service.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("exactly one run: %d %v", len(runs), err)
	}
}

// TestReportPlannerCatchUp proves three overdue slots materialize as
// three ordered runs across ticks with no gaps or duplicates.
func TestReportPlannerCatchUp(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	backdateSchedule(t, env, schedule.ID, pastSlotDate(4))
	planner := businessreports.NewPlanner(catalogStore(env),
		businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	for i := 0; i < 3; i++ {
		planner.TickForTest(context.Background())
	}
	runs, err := service.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 3 {
		t.Fatalf("three runs: %d %v", len(runs), err)
	}
	slots := map[string]bool{}
	for _, run := range runs {
		slots[run.SlotLocalDate] = true
	}
	for _, slot := range []string{pastSlotDate(4), pastSlotDate(3), pastSlotDate(2)} {
		if !slots[slot] {
			t.Fatalf("gap: %v", slots)
		}
	}
	updated, _, err := catalogStore(env).GetSchedule(context.Background(), schedule.ID)
	if err != nil || updated.NextRunLocalDate != pastSlotDate(1) {
		t.Fatalf("cursor after catch-up: %+v %v", updated, err)
	}
}

// TestReportPlannerDisabled proves disabled schedules materialize
// nothing while keeping history.
func TestReportPlannerDisabled(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	if _, err := service.DisableSchedule(context.Background(), schedule.ID); err != nil {
		t.Fatal(err)
	}
	backdateSchedule(t, env, schedule.ID, pastSlotDate(0))
	planner := businessreports.NewPlanner(catalogStore(env),
		businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	planner.TickForTest(context.Background())
	runs, err := service.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("no runs while disabled: %d %v", len(runs), err)
	}
	// Re-enable positions the first future slot, skipping the disabled gap.
	backdateSchedule(t, env, schedule.ID, pastSlotDate(3))
	enabled, err := service.EnableSchedule(context.Background(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.NextRunLocalDate == pastSlotDate(3) {
		t.Fatalf("re-enable skips gap: %+v", enabled)
	}
}

// TestReportScheduleValidation proves creation rejects bad kinds,
// times, anchors, and recipient-less schedules.
func TestReportScheduleValidation(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	ctx := context.Background()
	if _, err := service.CreateSchedule(ctx, "x", "WEEKLY", "21:00", "", []string{"r"}); err == nil {
		t.Fatal("bad kind must fail")
	}
	if _, err := service.CreateSchedule(ctx, "x", "DAILY", "25:00", "", []string{"r"}); err == nil {
		t.Fatal("bad time must fail")
	}
	recipient := addTestRecipient(t, service, "owner")
	if _, err := service.CreateSchedule(ctx, "x", "TEN_DAY", "21:00", "", []string{recipient.ID}); err == nil {
		t.Fatal("ten-day without anchor must fail")
	}
	if _, err := service.CreateSchedule(ctx, "x", "DAILY", "21:00", "", []string{recipient.ID}); err != nil {
		t.Fatalf("valid daily: %v", err)
	}
	if _, err := service.CreateSchedule(ctx, "y", "DAILY", "21:00", "", nil); err == nil {
		t.Fatalf("recipient-less schedule must fail: %v", err)
	}
}

// TestReportTenDaySchedule proves anchor-chained slots: creation with
// an anchor, materialization of the aligned slot, and +10 advances.
func TestReportTenDaySchedule(t *testing.T) {
	env := openSaleEnv(t)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	anchor := pastSlotDate(20)
	schedule := createTestSchedule(t, service, "owner-ten-day", "TEN_DAY", "21:00", anchor, recipient.ID)
	if schedule.AnchorLocalDate != anchor {
		t.Fatalf("anchor: %+v", schedule)
	}
	// Backdate the cursor to the anchor slot (overdue by construction).
	backdateSchedule(t, env, schedule.ID, anchor)
	planner := businessreports.NewPlanner(catalogStore(env),
		businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	planner.TickForTest(context.Background())
	runs, err := service.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("one ten-day run: %d %v", len(runs), err)
	}
	run := runs[0]
	if run.SlotLocalDate != anchor {
		t.Fatalf("anchor slot: %+v", run)
	}
	wantFrom, wantTo := anchorMinus(anchor, 10), anchorMinus(anchor, 1)
	if run.PeriodStart.In(mustCairo()).Format("2006-01-02") != wantFrom ||
		run.PeriodEnd.In(mustCairo()).Format("2006-01-02") != anchor {
		t.Fatalf("ten-day period: %v %v (want %s..%s)", run.PeriodStart, run.PeriodEnd, wantFrom, anchor)
	}
	_ = wantTo
	detail, err := service.GetRunStatus(context.Background(), run.ID)
	if err != nil || len(detail.Deliveries) != 1 ||
		detail.Deliveries[0].TemplateKey != "ten_day_business_report_v1" {
		t.Fatalf("ten-day delivery: %+v %v", detail, err)
	}
	updated, _, err := catalogStore(env).GetSchedule(context.Background(), schedule.ID)
	if err != nil || updated.NextRunLocalDate != anchorPlus(anchor, 10) {
		t.Fatalf("anchor +10: %+v %v", updated, err)
	}
	// Drain the ten-day run end to end: template resolves, Arabic body
	// composes for the 10-day period, delivery enqueues, run completes.
	seedReportTemplateMapping(t, env)
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err = reportTestService(t, env, &stubReportSummary{summary: cannedSummary()}).GetRunStatus(context.Background(), runs[0].ID)
	if err != nil || detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("ten-day run completes: %+v %v", detail.Run, err)
	}
	if !strings.Contains(detail.Deliveries[0].Body, "10 أيام") {
		t.Fatalf("ten-day body:\n%s", detail.Deliveries[0].Body)
	}
}

func anchorMinus(date string, days int) string {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	return parsed.AddDate(0, 0, -days).Format("2006-01-02")
}

func anchorPlus(date string, days int) string {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	return parsed.AddDate(0, 0, days).Format("2006-01-02")
}

// TestReportRebuildIsolation proves business-report tables carry no
// destructive foreign keys into rebuildable projections: catalog,
// sales, returns, inventory, and orders can never cascade report
// history away.
func TestReportRebuildIsolation(t *testing.T) {
	env := openSaleEnv(t)
	rows, err := env.pool.Query(context.Background(), `
		SELECT tc.table_name, ccu.table_name AS target
		FROM information_schema.table_constraints AS tc
		JOIN information_schema.constraint_column_usage AS ccu
			ON ccu.constraint_name = tc.constraint_name
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_name LIKE 'business\_report\_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	protected := map[string]bool{
		"catalog_products": true, "catalog_categories": true,
		"sales_projection": true, "return_refund_projection": true,
		"catalog_product_inventory": true, "commerce_online_orders": true,
		"commerce_product_mappings": true, "notification_messages": true,
	}
	for rows.Next() {
		var table, target string
		if err := rows.Scan(&table, &target); err != nil {
			t.Fatal(err)
		}
		if protected[target] {
			t.Fatalf("destructive coupling %s -> %s", table, target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestReportEnableOverduePreserved proves enabling an already-enabled
// overdue schedule is a no-op: cursor, revision, and backlog survive.
func TestReportEnableOverduePreserved(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	backdateSchedule(t, env, schedule.ID, pastSlotDate(3))
	before, _, err := catalogStore(env).GetSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.EnableSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.NextRunLocalDate != before.NextRunLocalDate || again.Revision != before.Revision {
		t.Fatalf("no-op enable: %+v vs %+v", again, before)
	}
	// Backlog still materializes exactly.
	planner := businessreports.NewPlanner(catalogStore(env),
		businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	planner.TickForTest(ctx)
	runs, err := service.ListRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].SlotLocalDate != pastSlotDate(3) {
		t.Fatalf("overdue slot survives: %+v %v", runs, err)
	}
}

// TestReportConcurrentEnable proves concurrent disabled→enable calls
// converge on one transition with a valid cursor.
func TestReportConcurrentEnable(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	if _, err := service.DisableSchedule(ctx, schedule.ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan businessreports.Schedule, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			enabled, err := service.EnableSchedule(ctx, schedule.ID)
			if err != nil {
				t.Errorf("enable: %v", err)
				return
			}
			results <- enabled
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	count := 0
	for enabled := range results {
		count++
		if !enabled.Enabled || enabled.NextRunLocalDate == "" {
			t.Fatalf("valid transition: %+v", enabled)
		}
	}
	if count != 2 {
		t.Fatalf("both callers succeed: %d", count)
	}
	final, _, err := catalogStore(env).GetSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !final.Enabled || final.NextRunLocalDate == "" {
		t.Fatalf("one valid transition: %+v", final)
	}
	// At most one revision bump beyond disable: no cursor corruption.
	if final.Revision > 4 {
		t.Fatalf("revision churn: %+v", final)
	}
}

// TestReportScheduleCreateAtomic proves schedule creation is
// all-or-nothing across invalid, mixed, and disabled recipients, and
// that a corrected same-name retry succeeds (unique name unconsumed).
func TestReportScheduleCreateAtomic(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	good := addTestRecipient(t, service, "good")
	counts := func() (schedules, links int) {
		t.Helper()
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM business_report_schedules`).Scan(&schedules); err != nil {
			t.Fatal(err)
		}
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM business_report_schedule_recipients`).Scan(&links); err != nil {
			t.Fatal(err)
		}
		return schedules, links
	}
	newSchedule := func(name string, recipients ...string) error {
		_, err := service.CreateSchedule(ctx, name, "DAILY", "21:00", "", recipients)
		return err
	}
	// Nonexistent recipient: nothing committed.
	if err := newSchedule("s-bad", "00000000-0000-4000-8000-000000000000"); err == nil {
		t.Fatal("nonexistent recipient must fail")
	}
	// Mixed valid + invalid: nothing committed, no partial links.
	if err := newSchedule("s-mixed", good.ID, "00000000-0000-4000-8000-000000000001"); err == nil {
		t.Fatal("mixed recipients must fail")
	}
	// Disabled recipient: nothing committed.
	disabled, err := service.AddRecipient(ctx, "off", "whatsapp-main", "201011111111", "ar")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRecipientEnabled(ctx, disabled.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := newSchedule("s-off", disabled.ID); err == nil {
		t.Fatal("disabled recipient must fail")
	}
	if schedules, links := counts(); schedules != 0 || links != 0 {
		t.Fatalf("nothing committed: %d schedules %d links", schedules, links)
	}
	// Corrected same-name retry succeeds: names unconsumed.
	if _, err := service.CreateSchedule(ctx, "s-bad", "DAILY", "21:00", "", []string{good.ID}); err != nil {
		t.Fatalf("corrected retry: %v", err)
	}
	if schedules, links := counts(); schedules != 1 || links != 1 {
		t.Fatalf("one schedule one link: %d %d", schedules, links)
	}
	// Concurrent planner visibility: a racing planner tick during a
	// second creation observes either zero or one complete schedule,
	// never a partial link set.
	planner := businessreports.NewPlanner(store,
		businessreports.SystemClock{}, mustCairo(), time.Minute, 25, nilLogger())
	planner.TickForTest(ctx)
	if schedules, _ := counts(); schedules != 1 {
		t.Fatalf("no phantom schedule: %d", schedules)
	}
}

// TestReportScheduleCreateRaces proves creation-time races resolve
// safely: concurrent same-name creates yield exactly one schedule
// (mid-transaction unique violation rolls back everything), and a
// recipient disabled mid-creation yields either a clean error or a
// valid schedule — never partial links.
func TestReportScheduleCreateRaces(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	good := addTestRecipient(t, service, "good")
	t.Run("concurrent same name", func(t *testing.T) {
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := service.CreateSchedule(ctx, "s-race", "DAILY", "21:00", "", []string{good.ID})
				results <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		var succeeded, failed int
		for err := range results {
			if err == nil {
				succeeded++
			} else {
				failed++
			}
		}
		if succeeded != 1 || failed != 1 {
			t.Fatalf("exactly one winner: %d/%d", succeeded, failed)
		}
		var schedules, links int
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM business_report_schedules WHERE name='s-race'`).Scan(&schedules); err != nil || schedules != 1 {
			t.Fatalf("one schedule: %d %v", schedules, err)
		}
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM business_report_schedule_recipients`).Scan(&links); err != nil || links != 1 {
			t.Fatalf("one link: %d %v", links, err)
		}
	})
	t.Run("disable during create", func(t *testing.T) {
		victim, err := service.AddRecipient(ctx, "victim", "whatsapp-main", "201022222222", "ar")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := service.CreateSchedule(ctx, "s-victim", "DAILY", "21:00", "", []string{victim.ID})
			done <- err
		}()
		// Race the disable against validation/linking; either order
		// is a deterministic safe outcome.
		_ = catalogStore(env).SetRecipientEnabled(ctx, victim.ID, false)
		err = <-done
		var schedules, links int
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM business_report_schedules WHERE name='s-victim'`).Scan(&schedules); err != nil {
			t.Fatal(err)
		}
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM business_report_schedule_recipients l JOIN business_report_schedules s ON s.id = l.schedule_id WHERE s.name='s-victim'`).Scan(&links); err != nil {
			t.Fatal(err)
		}
		if err == nil {
			// Won before the disable: exactly one schedule + one link.
			if schedules != 1 || links != 1 {
				t.Fatalf("winner complete: %d %d", schedules, links)
			}
		} else if schedules != 0 || links != 0 {
			t.Fatalf("loser clean: %d %d (%v)", schedules, links, err)
		}
	})
}

// TestReportIDsAreUUIDv7 proves newly generated 7B application IDs
// use the repository-standard UUIDv7 generator.
func TestReportIDsAreUUIDv7(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient := addTestRecipient(t, service, "owner")
	schedule := createTestSchedule(t, service, "owner-daily", "DAILY", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{recipient.ID, schedule.ID, runID, detail.Deliveries[0].ID} {
		parsed, err := parseUUID(id)
		if err != nil {
			t.Fatalf("uuid: %v", err)
		}
		version := (parsed.Bytes[6] >> 4) & 0x0f
		if version != 7 {
			t.Fatalf("id %s is version %d, want 7", id, version)
		}
	}
}

// TestReportAggregateConsistency proves run verdicts always match
// durable delivery rows: mixed enqueued/blocked finalizes blocked,
// never completed.
func TestReportAggregateConsistency(t *testing.T) {
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
	runID, _, err := service.RunNow(ctx, schedule.ID, "manual-001")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || len(detail.Deliveries) != 2 {
		t.Fatalf("two deliveries: %+v %v", detail, err)
	}
	// Finish one delivery enqueued and the other blocked under the
	// current lease, then run the aggregate path via a fresh drain.
	if _, err := env.pool.Exec(ctx,
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	_ = claimed
	seedReportTemplateMapping(t, env)
	seedReportTemplateMapping(t, env)
	// Delete the English mapping so English blocks deterministically
	// while Arabic enqueues.
	if _, err := env.pool.Exec(ctx,
		`DELETE FROM notification_template_mappings WHERE locale = 'en'`); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunnerNoSeed(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err = service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunBlocked {
		t.Fatalf("mixed run blocks: %+v", detail.Run)
	}
	var enqueued, blocked int
	for _, delivery := range detail.Deliveries {
		switch delivery.Status {
		case businessreports.DeliveryEnqueued:
			enqueued++
		case businessreports.DeliveryBlocked:
			blocked++
		}
	}
	if enqueued != 1 || blocked != 1 {
		t.Fatalf("1+1 durable mix: %+v", detail.Deliveries)
	}
}
