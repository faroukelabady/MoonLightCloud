package postgres

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

func TestReportConfigurationPreservesQueuedHistory(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	source := &stubReportSummary{summary: cannedSummary()}
	service := reportTestService(t, env, source)
	recipient := addTestRecipient(t, service, "Original recipient")
	schedule := createTestSchedule(t, service, "Original daily", "daily", "21:00", "", recipient.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "before-edit")
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	updatedRecipient, err := service.UpdateRecipient(ctx, recipient.ID, "Updated recipient", "201098765432", "en")
	if err != nil {
		t.Fatal(err)
	}
	if updatedRecipient.ID != recipient.ID || updatedRecipient.ProviderKey != recipient.ProviderKey {
		t.Fatal("recipient identity/provider changed")
	}
	updated, err := service.UpdateSchedule(ctx, schedule.ID, "Updated ten-day", "ten_day", "20:00", pastSlotDate(-1), []string{recipient.ID}, schedule.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != schedule.ID || updated.Revision != schedule.Revision+1 {
		t.Fatal("schedule identity/revision mismatch")
	}
	if _, err := service.UpdateSchedule(ctx, schedule.ID, "Stale edit", "daily", "19:00", "", []string{recipient.ID}, schedule.Revision); err == nil {
		t.Fatal("stale editor overwrote schedule")
	}
	drainRunner(t, testRunner(t, env, source))
	after, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Run.ReportKind != businessreports.ReportDaily || after.Run.ScheduleName != "Original daily" {
		t.Fatal("run snapshot followed edited schedule")
	}
	if len(after.Deliveries) != 1 || after.Deliveries[0].Recipient != recipient.Recipient || after.Deliveries[0].Locale != "ar" || after.Deliveries[0].TemplateKey != businessreports.TemplateDaily {
		t.Fatal("queued delivery followed edited recipient")
	}
	if !strings.Contains(after.Deliveries[0].Body, "تقرير المبيعات اليومي") {
		t.Fatal("queued daily report was rendered as a ten-day report")
	}
	if before.Deliveries[0].ID != after.Deliveries[0].ID {
		t.Fatal("queued delivery replaced")
	}
	newRun, _, err := service.RunNow(ctx, schedule.ID, "after-edit")
	if err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, source))
	newDetail, err := service.GetRunStatus(ctx, newRun)
	if err != nil {
		t.Fatal(err)
	}
	if newDetail.Run.ReportKind != businessreports.ReportTenDay || newDetail.Deliveries[0].Recipient != "201098765432" || newDetail.Deliveries[0].Locale != "en" {
		t.Fatal("future run did not use edited configuration")
	}
	originalBody, originalFP := after.Deliveries[0].Body, append([]byte(nil), after.Deliveries[0].Fingerprint...)
	if err := service.DeleteRecipient(ctx, recipient.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSchedule(ctx, schedule.ID); err != nil {
		t.Fatal(err)
	}
	if recipients, err := service.ListRecipients(ctx); err != nil || len(recipients) != 0 {
		t.Fatalf("removed recipient still listed: %v", err)
	}
	if schedules, err := service.ListSchedules(ctx); err != nil || len(schedules) != 0 {
		t.Fatalf("removed schedule still listed: %v", err)
	}
	if _, _, err := service.RunNow(ctx, schedule.ID, "deleted-run"); err == nil {
		t.Fatal("deleted schedule accepted a manual run")
	}
	if _, err := service.EnableSchedule(ctx, schedule.ID); err == nil {
		t.Fatal("deleted schedule revived")
	}
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Deliveries[0].Body != originalBody || !bytes.Equal(detail.Deliveries[0].Fingerprint, originalFP) {
		t.Fatal("delete changed historical report")
	}
	var notificationCount int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages`).Scan(&notificationCount); err != nil || notificationCount != 2 {
		t.Fatalf("notification history removed: %d %v", notificationCount, err)
	}
	// A deleted configuration's name can be reused without erasing its history.
	r := addTestRecipient(t, service, "Replacement")
	createTestSchedule(t, service, "Updated ten-day", "daily", "21:00", "", r.ID)
}

func TestDeletedScheduleKeepsPendingRunExecutable(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	source := &stubReportSummary{summary: cannedSummary()}
	service := reportTestService(t, env, source)
	r := addTestRecipient(t, service, "Owner")
	s := createTestSchedule(t, service, "Queued then removed", "ten_day", "21:00", pastSlotDate(-1), r.ID)
	id, _, err := service.RunNow(ctx, s.ID, "queued-before-delete")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSchedule(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteRecipient(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, source))
	detail, err := service.GetRunStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != businessreports.RunCompleted || !detail.Deliveries[0].HasBody {
		t.Fatal("deleting config broke previously queued work")
	}
}

func TestReportRunPaginationStableAcrossNewRuns(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	r := addTestRecipient(t, service, "Owner")
	s := createTestSchedule(t, service, "History", "daily", "21:00", "", r.ID)
	for i := 0; i < 23; i++ {
		if _, _, err := service.RunNow(ctx, s.ID, fmt.Sprintf("page-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.ListRunPage(ctx, "", 10)
	if err == nil && first.RemainingRuns != 13 {
		t.Fatalf("remaining runs: %d", first.RemainingRuns)
	}
	if err != nil || len(first.Runs) != 10 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if _, _, err := service.RunNow(ctx, s.ID, "new-while-paging"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, run := range first.Runs {
		seen[run.ID] = true
	}
	second, err := service.ListRunPage(ctx, first.NextCursor, 10)
	if err == nil && second.RemainingRuns != 3 {
		t.Fatalf("remaining older runs: %d", second.RemainingRuns)
	}
	if err != nil || len(second.Runs) != 10 {
		t.Fatalf("second page: %v", err)
	}
	for _, run := range second.Runs {
		if seen[run.ID] {
			t.Fatal("new run shifted pagination into a duplicate")
		}
		seen[run.ID] = true
	}
	third, err := service.ListRunPage(ctx, second.NextCursor, 10)
	if err == nil && third.RemainingRuns != 0 {
		t.Fatalf("final page remaining: %d", third.RemainingRuns)
	}
	if err != nil || len(third.Runs) != 3 || third.NextCursor != "" {
		t.Fatalf("last page: %+v %v", third, err)
	}
	for _, run := range third.Runs {
		if seen[run.ID] {
			t.Fatal("duplicate on last page")
		}
		seen[run.ID] = true
	}
	if len(seen) != 23 {
		t.Fatal("history row lost")
	}
	if _, err := service.ListRunPage(ctx, "invalid", 10); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if _, err := service.ListRunPage(ctx, "", 1000); err == nil {
		t.Fatal("unbounded page accepted")
	}
}

func TestScheduleEditInvalidRecipientRollsBack(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	r := addTestRecipient(t, service, "Owner")
	s := createTestSchedule(t, service, "Original", "daily", "21:00", "", r.ID)
	if _, err := service.UpdateSchedule(ctx, s.ID, "Changed", "daily", "20:00", "", []string{"not-a-uuid"}, s.Revision); err == nil {
		t.Fatal("invalid recipient accepted")
	}
	stored, _, err := businessReportStore(env).GetSchedule(ctx, s.ID)
	if err != nil || stored.Name != s.Name || stored.LocalTime != s.LocalTime || stored.Revision != s.Revision {
		t.Fatal("invalid edit partially changed schedule")
	}
	links, err := service.ScheduleRecipients(ctx, s.ID)
	if err != nil || len(links) != 1 || links[0].ID != r.ID {
		t.Fatal("invalid edit partially changed links")
	}
}

func TestReportHistoryReadsRealNotificationOutcomesWithoutChangingRun(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	source := &stubReportSummary{summary: cannedSummary()}
	service := reportTestService(t, env, source)
	wa := addTestRecipient(t, service, "WA owner")
	tg, err := service.AddRecipient(ctx, "TG owner", "telegram-main", "123456789", "ar")
	if err != nil {
		t.Fatal(err)
	}
	seedTelegramTemplateMapping(t, env)
	schedule := createTestSchedule(t, service, "Delivery history", "daily", "21:00", "", wa.ID, tg.ID)
	runID, _, err := service.RunNow(ctx, schedule.ID, "delivery-history")
	if err != nil {
		t.Fatal(err)
	}
	drainRunner(t, testRunner(t, env, source))
	states := func() map[string]businessreports.DeliveryState {
		t.Helper()
		page, err := service.ListRunPage(ctx, "", 10)
		if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != runID || page.Runs[0].Status != businessreports.RunCompleted {
			t.Fatalf("report handoff state changed: %+v %v", page, err)
		}
		out := map[string]businessreports.DeliveryState{}
		for _, state := range page.Runs[0].DeliveryStates {
			out[state.ProviderKey] = state
		}
		if len(out) != 2 || out["whatsapp-main"].RecipientMasked != "…5678" || out["telegram-main"].RecipientMasked != "…6789" {
			t.Fatalf("missing or unmasked delivery metadata: %+v", out)
		}
		return out
	}
	for _, state := range states() {
		if state.DispatchStatus != "pending" || state.DeliveryStatus != "unknown" {
			t.Fatalf("handoff falsely counted as sent: %+v", state)
		}
	}
	n := NewNotifications(env.pool, 5*time.Second)
	for i := 0; i < 2; i++ {
		claim, found, err := n.ClaimNotification(ctx, "status-test", time.Minute, time.Now())
		if err != nil || !found {
			t.Fatalf("claim: %v %v", found, err)
		}
		if _, err := n.MarkNotificationSendStarted(ctx, claim.ID, claim.LeaseOwner, claim.LeaseGeneration); err != nil {
			t.Fatal(err)
		}
		if !states()[claim.ProviderKey].SendStarted {
			t.Fatal("active send not exposed")
		}
		if claim.ProviderKey == "telegram-main" {
			if _, err := n.FinishNotificationBlocked(ctx, claim.ID, claim.LeaseOwner, claim.LeaseGeneration, "AUTHENTICATION"); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := n.FinishNotificationAccepted(ctx, claim.ID, claim.LeaseOwner, claim.LeaseGeneration, "wamid.status-test"); err != nil {
				t.Fatal(err)
			}
			if states()[claim.ProviderKey].DeliveryStatus != "accepted" {
				t.Fatal("provider acceptance hidden")
			}
			for i, status := range []notifications.DeliveryStatus{notifications.DeliveryDelivered, notifications.DeliveryRead} {
				at := time.Now().Add(time.Duration(i+1) * time.Second)
				event := notifications.DeliveryEvent{ProviderMessageID: "wamid.status-test", RawStatus: string(status), Canonical: status, ProviderTimestamp: &at}
				event.Fingerprint = notifications.DeliveryEventFingerprint(claim.ProviderKey, event.ProviderMessageID, event.RawStatus, status, &at, "")
				if _, err := n.ApplyDeliveryStatus(ctx, claim.ID, event); err != nil {
					t.Fatal(err)
				}
				if states()[claim.ProviderKey].DeliveryStatus != string(status) {
					t.Fatalf("webhook outcome %s hidden", status)
				}
			}
		}
	}
	got := states()
	if got["telegram-main"].DispatchStatus != "blocked" || got["telegram-main"].LastErrorCode != "AUTHENTICATION" || got["whatsapp-main"].DeliveryStatus != "read" {
		t.Fatalf("mixed outcomes lost: %+v", got)
	}
}

func TestReportHistoryDoesNotRevealShortChatIDs(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	r, err := service.AddRecipient(ctx, "Short chat", "telegram-main", "123", "ar")
	if err != nil {
		t.Fatal(err)
	}
	s := createTestSchedule(t, service, "Short-chat privacy", "daily", "21:00", "", r.ID)
	if _, _, err := service.RunNow(ctx, s.ID, "short-chat-status"); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListRunPage(ctx, "", 10)
	if err != nil || len(page.Runs) != 1 || len(page.Runs[0].DeliveryStates) != 1 || page.Runs[0].DeliveryStates[0].RecipientMasked != "…" {
		t.Fatalf("short chat ID revealed: %+v %v", page, err)
	}
}
