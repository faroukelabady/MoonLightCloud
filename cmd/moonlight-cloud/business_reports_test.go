package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
)

func errTestNotFound() error { return apperr.New(apperr.NotFound, "not found") }

func nilRunner() *businessreports.Runner {
	loc, err := businessreports.LoadCairo()
	if err != nil {
		panic(err)
	}
	return businessreports.NewRunner(nil, nil, nil, nil,
		businessreports.SystemClock{}, loc, time.Minute, 25,
		"test", time.Minute, nil)
}

var _ = errors.New

// memReportService is an in-memory business-report service boundary
// for CLI tests with real masking/privacy semantics.
type memReportService struct {
	recipients map[string]businessreports.Recipient
	schedules  map[string]businessreports.Schedule
	runs       map[string]businessreports.Run
	deliveries map[string][]businessreports.Delivery
}

func newMemReportService() *memReportService {
	return &memReportService{
		recipients: map[string]businessreports.Recipient{},
		schedules:  map[string]businessreports.Schedule{},
		runs:       map[string]businessreports.Run{},
		deliveries: map[string][]businessreports.Delivery{},
	}
}

func (s *memReportService) AddRecipient(_ context.Context, label, providerKey, recipient, locale string) (businessreports.Recipient, error) {
	record := businessreports.Recipient{
		ID: "recipient-1", Label: label, ProviderKey: providerKey,
		Recipient: recipient, Locale: locale, Enabled: true,
	}
	s.recipients[record.ID] = record
	return record, nil
}

func (s *memReportService) ListRecipients(_ context.Context) ([]businessreports.Recipient, error) {
	var out []businessreports.Recipient
	for _, recipient := range s.recipients {
		out = append(out, recipient)
	}
	return out, nil
}

func (s *memReportService) DisableRecipient(_ context.Context, id string) error {
	record := s.recipients[id]
	record.Enabled = false
	s.recipients[id] = record
	return nil
}

func (s *memReportService) CreateSchedule(_ context.Context, name, kind, localTime, anchor string, recipientIDs []string) (businessreports.Schedule, error) {
	schedule := businessreports.Schedule{
		ID: "schedule-1", Name: name, Kind: businessreports.ReportKind(kind),
		Timezone: businessreports.TimezoneCairo, LocalTime: localTime,
		AnchorLocalDate: anchor, Enabled: true, Revision: 1,
		NextRunLocalDate: "2026-09-29", NextRunAt: time.Now().UTC(),
	}
	s.schedules[schedule.ID] = schedule
	return schedule, nil
}

func (s *memReportService) ListSchedules(_ context.Context) ([]businessreports.Schedule, error) {
	var out []businessreports.Schedule
	for _, schedule := range s.schedules {
		out = append(out, schedule)
	}
	return out, nil
}

func (s *memReportService) EnableSchedule(_ context.Context, id string) (businessreports.Schedule, error) {
	schedule := s.schedules[id]
	schedule.Enabled = true
	s.schedules[id] = schedule
	return schedule, nil
}

func (s *memReportService) DisableSchedule(_ context.Context, id string) (businessreports.Schedule, error) {
	schedule := s.schedules[id]
	schedule.Enabled = false
	s.schedules[id] = schedule
	return schedule, nil
}

func (s *memReportService) RunNow(_ context.Context, scheduleID, manualKey string) (string, bool, error) {
	if existing, ok := s.runs[manualKey]; ok {
		return existing.ID, false, nil
	}
	run := businessreports.Run{ID: "run-1", ScheduleID: scheduleID, Kind: businessreports.RunManual,
		Status: businessreports.RunPending}
	s.runs[manualKey] = run
	return run.ID, true, nil
}

func (s *memReportService) ListRuns(_ context.Context, _ int32) ([]businessreports.Run, error) {
	var out []businessreports.Run
	for _, run := range s.runs {
		out = append(out, run)
	}
	return out, nil
}

func (s *memReportService) GetRunStatus(_ context.Context, id string) (businessreports.RunDetail, error) {
	for _, run := range s.runs {
		if run.ID == id {
			return businessreports.RunDetail{Run: run, Deliveries: s.deliveries[id]}, nil
		}
	}
	return businessreports.RunDetail{}, errTestNotFound()
}

func TestBusinessReportsRecipientCLI(t *testing.T) {
	ctx := context.Background()
	service := newMemReportService()
	var out bytes.Buffer
	if err := runRecipientAdd(ctx, service, &out, "owner", "whatsapp-main", "201012345678", "ar"); err != nil {
		t.Fatalf("add: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "id=recipient-1") || !strings.Contains(text, "recipient=...5678") {
		t.Fatalf("masked add: %q", text)
	}
	if strings.Contains(text, "201012345678") {
		t.Fatalf("full recipient leaked: %q", text)
	}
	out.Reset()
	if err := runRecipientList(ctx, service, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "count=1") || strings.Contains(out.String(), "201012345678") {
		t.Fatalf("masked list: %q", out.String())
	}
	out.Reset()
	if err := runRecipientDisable(ctx, service, &out, "recipient-1"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if service.recipients["recipient-1"].Enabled {
		t.Fatal("disabled")
	}
}

func TestBusinessReportsScheduleCLI(t *testing.T) {
	ctx := context.Background()
	service := newMemReportService()
	var out bytes.Buffer
	if err := runScheduleCreate(ctx, service, &out, "owner-daily", "DAILY", "21:00", "", []string{"recipient-1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out.String(), "id=schedule-1") || !strings.Contains(out.String(), "next=2026-09-29") {
		t.Fatalf("create output: %q", out.String())
	}
	out.Reset()
	if err := runScheduleList(ctx, service, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "count=1") {
		t.Fatalf("list output: %q", out.String())
	}
	out.Reset()
	if err := runScheduleToggle(ctx, service, &out, "disable", "schedule-1"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if service.schedules["schedule-1"].Enabled {
		t.Fatal("disabled")
	}
}

func TestBusinessReportsRunCLI(t *testing.T) {
	ctx := context.Background()
	service := newMemReportService()
	var out bytes.Buffer
	if err := runReportNow(ctx, service, nilRunner(), &out, "schedule-1", "manual-001"); err != nil {
		t.Fatalf("run-now: %v", err)
	}
	if !strings.Contains(out.String(), "run=run-1 created=true") {
		t.Fatalf("run-now output: %q", out.String())
	}
	out.Reset()
	if err := runRunsList(ctx, service, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "run=run-1") {
		t.Fatalf("runs list: %q", out.String())
	}
	service.deliveries["run-1"] = []businessreports.Delivery{{
		ID: "delivery-1", RecipientID: "recipient-1", ProviderKey: "whatsapp-main",
		Recipient: "201012345678", Locale: "ar", TemplateKey: "daily_business_report_v1",
		Status: businessreports.DeliveryEnqueued, NotificationID: "notif-1",
	}}
	out.Reset()
	if err := runRunStatus(ctx, service, &out, "run-1"); err != nil {
		t.Fatalf("status: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "enqueued=1") || !strings.Contains(text, "recipient=...5678") {
		t.Fatalf("masked status: %q", text)
	}
	if strings.Contains(text, "201012345678") {
		t.Fatalf("full recipient leaked: %q", text)
	}
}
