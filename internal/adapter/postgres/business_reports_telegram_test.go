package postgres

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// TestTelegramReportRunnerEndToEnd runs the real Phase 7B chain with a
// Telegram recipient on PostgreSQL: schedule, run-now, deterministic
// report computation, runner enqueue, lease-fenced dispatch, Telegram
// send. Financial computation is untouched (same canned summary the
// WhatsApp path uses); only the provider leg differs. Skips without
// TEST_DATABASE_URL.
func TestTelegramReportRunnerEndToEnd(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	seedTelegramTemplateMapping(t, env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})

	recipient, err := service.AddRecipient(ctx, "owner-tg", "telegram-main", "@operations", "ar")
	if err != nil {
		t.Fatalf("telegram recipient: %v", err)
	}
	schedule := createTestSchedule(t, service, "owner-daily-tg", "DAILY", "21:00", "", recipient.ID)
	runID, created, err := service.RunNow(ctx, schedule.ID, "manual-tg-001")
	if err != nil || !created {
		t.Fatalf("run-now: %v %v", runID, err)
	}
	drainRunner(t, testRunner(t, env, &stubReportSummary{summary: cannedSummary()}))
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunCompleted {
		t.Fatalf("completed: %+v", detail.Run)
	}
	deliveries := runDeliveries(t, service, runID)
	if len(deliveries) != 1 {
		t.Fatalf("one delivery, got %d", len(deliveries))
	}
	if deliveries[0].NotificationID == "" {
		t.Fatal("runner must link the durable notification")
	}

	store := catalogStore(env)
	stub := newBotStub(t)
	notifications.NewDispatcher(store,
		telegramTestRegistry(t, telegramTestProvider(t, stub)),
		notifications.SystemClock{}, "worker-T", slog.Default()).DrainForTest(ctx)

	status, err := store.GetNotificationStatus(ctx, deliveries[0].NotificationID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Dispatch != notifications.DispatchAccepted {
		t.Fatalf("dispatch: %q code %q", status.Dispatch, status.LastErrorCode)
	}
	if status.Delivery != notifications.DeliveryAccepted {
		t.Fatalf("delivery truth at most ACCEPTED: %q", status.Delivery)
	}
	if stub.count() != 1 {
		t.Fatalf("one remote send, got %d", stub.count())
	}
	var body string
	stub.mu.Lock()
	if len(stub.bodies) > 0 {
		body = stub.bodies[0]
	}
	stub.mu.Unlock()
	if !strings.Contains(body, deliveries[0].Body) || deliveries[0].Body == "" {
		t.Fatal("telegram text must equal the canonical report body")
	}
}
