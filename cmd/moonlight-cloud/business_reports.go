package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// businessReportsCmd is the operator surface for scheduled business
// reports: recipients, schedules, manual runs, and run inspection. No
// bulk send, no marketing tooling, no report content display.
func businessReportsCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud business-reports recipients|schedules|run-now|runs ...")
	}
	switch args[0] {
	case "recipients":
		return reportRecipientsCmd(args[1:], stdout, stderr)
	case "schedules":
		return reportSchedulesCmd(args[1:], stdout, stderr)
	case "run-now":
		return reportRunNowCmd(args[1:], stdout, stderr)
	case "runs":
		return reportRunsCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud business-reports recipients|schedules|run-now|runs ...")
	}
}

// maskRecipient shows only the last four characters for operator
// display. Full recipients never appear in CLI output.
func maskRecipient(recipient string) string {
	if len(recipient) <= 4 {
		return "..."
	}
	return "..." + recipient[len(recipient)-4:]
}

func reportRecipientsCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud business-reports recipients add|list|disable ...")
	}
	switch args[0] {
	case "add":
		return reportRecipientAddCmd(args[1:], stdout, stderr)
	case "list":
		return reportRecipientListCmd(args[1:], stdout, stderr)
	case "disable":
		return reportRecipientDisableCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud business-reports recipients add|list|disable ...")
	}
}

func reportRecipientAddCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports recipients add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	label := fs.String("label", "", "operator label")
	providerKey := fs.String("provider", "", "notification provider key")
	recipient := fs.String("recipient", "", "canonical international recipient")
	locale := fs.String("locale", "", "ar|en")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*label) == "" || strings.TrimSpace(*providerKey) == "" ||
		strings.TrimSpace(*recipient) == "" || strings.TrimSpace(*locale) == "" {
		return fmt.Errorf("usage: moonlight-cloud business-reports recipients add --label <label> --provider <key> --recipient <addr> --locale <ar|en>")
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runRecipientAdd(context.Background(), env.service, stdout,
		strings.TrimSpace(*label), strings.TrimSpace(*providerKey),
		strings.TrimSpace(*recipient), strings.TrimSpace(*locale))
}

// recipientService is the narrow recipient boundary for CLI.
type recipientService interface {
	AddRecipient(ctx context.Context, label, providerKey, recipient, locale string) (businessreports.Recipient, error)
	ListRecipients(ctx context.Context) ([]businessreports.Recipient, error)
	DisableRecipient(ctx context.Context, id string) error
}

// runRecipientAdd creates one recipient and prints masked identity.
func runRecipientAdd(ctx context.Context, service recipientService, out io.Writer, label, providerKey, recipient, locale string) error {
	record, err := service.AddRecipient(ctx, label, providerKey, recipient, locale)
	if err != nil {
		fmt.Fprintf(out, "error=%s\n", err.Error())
		return err
	}
	fmt.Fprintf(out, "id=%s label=%s provider=%s locale=%s recipient=%s\n",
		record.ID, record.Label, record.ProviderKey, record.Locale, maskRecipient(record.Recipient))
	return nil
}

func reportRecipientListCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports recipients list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runRecipientList(context.Background(), env.service, stdout)
}

// runRecipientList prints masked recipients.
func runRecipientList(ctx context.Context, service recipientService, out io.Writer) error {
	recipients, err := service.ListRecipients(ctx)
	if err != nil {
		return err
	}
	for _, recipient := range recipients {
		fmt.Fprintf(out, "id=%s label=%s provider=%s locale=%s enabled=%v recipient=%s\n",
			recipient.ID, recipient.Label, recipient.ProviderKey,
			recipient.Locale, recipient.Enabled, maskRecipient(recipient.Recipient))
	}
	fmt.Fprintf(out, "count=%d\n", len(recipients))
	return nil
}

func reportRecipientDisableCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports recipients disable", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "recipient UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("usage: moonlight-cloud business-reports recipients disable --id <uuid>")
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runRecipientDisable(context.Background(), env.service, stdout, strings.TrimSpace(*id))
}

// runRecipientDisable disables one recipient by ID.
func runRecipientDisable(ctx context.Context, service recipientService, out io.Writer, id string) error {
	if err := service.DisableRecipient(ctx, id); err != nil {
		fmt.Fprintf(out, "error=%s\n", err.Error())
		return err
	}
	fmt.Fprintf(out, "id=%s enabled=false\n", id)
	return nil
}

func reportSchedulesCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud business-reports schedules create|list|enable|disable ...")
	}
	switch args[0] {
	case "create":
		return reportScheduleCreateCmd(args[1:], stdout, stderr)
	case "list":
		return reportScheduleListCmd(args[1:], stdout, stderr)
	case "enable":
		return reportScheduleEnableCmd(args[1:], stdout, stderr)
	case "disable":
		return reportScheduleDisableCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud business-reports schedules create|list|enable|disable ...")
	}
}

// recipientFlags collects repeatable --recipient <uuid> links.
type recipientFlags []string

func (f *recipientFlags) String() string { return strings.Join(*f, ",") }

func (f *recipientFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("invalid --recipient: want a recipient UUID")
	}
	*f = append(*f, value)
	return nil
}

func reportScheduleCreateCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports schedules create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "schedule name")
	kind := fs.String("kind", "", "daily|ten-day")
	localTime := fs.String("time", "", "local HH:MM Africa/Cairo")
	anchor := fs.String("anchor-date", "", "TEN_DAY anchor YYYY-MM-DD")
	var recipients recipientFlags
	fs.Var(&recipients, "recipient", "recipient UUID (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizedKind := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(*kind), "-", "_"))
	if strings.TrimSpace(*name) == "" || normalizedKind == "" || strings.TrimSpace(*localTime) == "" {
		return fmt.Errorf("usage: moonlight-cloud business-reports schedules create --name <name> --kind <daily|ten-day> --time <HH:MM> [--anchor-date <YYYY-MM-DD>] --recipient <uuid> ...")
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runScheduleCreate(context.Background(), env.service, stdout,
		strings.TrimSpace(*name), normalizedKind, strings.TrimSpace(*localTime),
		strings.TrimSpace(*anchor), recipients)
}

// scheduleService is the narrow schedule boundary for CLI.
type scheduleService interface {
	CreateSchedule(ctx context.Context, name, kind, localTime, anchor string, recipientIDs []string) (businessreports.Schedule, error)
	ListSchedules(ctx context.Context) ([]businessreports.Schedule, error)
	EnableSchedule(ctx context.Context, id string) (businessreports.Schedule, error)
	DisableSchedule(ctx context.Context, id string) (businessreports.Schedule, error)
}

// runScheduleCreate stores one schedule and prints its safe summary.
func runScheduleCreate(ctx context.Context, service scheduleService, out io.Writer, name, kind, localTime, anchor string, recipients []string) error {
	schedule, err := service.CreateSchedule(ctx, name, kind, localTime, anchor, recipients)
	if err != nil {
		fmt.Fprintf(out, "error=%s\n", err.Error())
		return err
	}
	fmt.Fprintf(out, "id=%s name=%s kind=%s time=%s next=%s\n",
		schedule.ID, schedule.Name, schedule.Kind, schedule.LocalTime, schedule.NextRunLocalDate)
	return nil
}

func reportScheduleListCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports schedules list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runScheduleList(context.Background(), env.service, stdout)
}

// runScheduleList prints safe schedule summaries.
func runScheduleList(ctx context.Context, service scheduleService, out io.Writer) error {
	schedules, err := service.ListSchedules(ctx)
	if err != nil {
		return err
	}
	for _, schedule := range schedules {
		fmt.Fprintf(out, "id=%s name=%s kind=%s time=%s enabled=%v next=%s revision=%d\n",
			schedule.ID, schedule.Name, schedule.Kind, schedule.LocalTime,
			schedule.Enabled, schedule.NextRunLocalDate, schedule.Revision)
	}
	fmt.Fprintf(out, "count=%d\n", len(schedules))
	return nil
}

func reportScheduleEnableCmd(args []string, stdout, stderr io.Writer) error {
	return reportScheduleToggleCmd("enable", args, stdout, stderr)
}

func reportScheduleDisableCmd(args []string, stdout, stderr io.Writer) error {
	return reportScheduleToggleCmd("disable", args, stdout, stderr)
}

func reportScheduleToggleCmd(action string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports schedules "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "schedule UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("usage: moonlight-cloud business-reports schedules %s --id <uuid>", action)
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runScheduleToggle(context.Background(), env.service, stdout, action, strings.TrimSpace(*id))
}

// runScheduleToggle enables or disables one schedule.
func runScheduleToggle(ctx context.Context, service scheduleService, out io.Writer, action, id string) error {
	var schedule businessreports.Schedule
	var err error
	if action == "enable" {
		schedule, err = service.EnableSchedule(ctx, id)
	} else {
		schedule, err = service.DisableSchedule(ctx, id)
	}
	if err != nil {
		fmt.Fprintf(out, "error=%s\n", err.Error())
		return err
	}
	fmt.Fprintf(out, "id=%s enabled=%v next=%s revision=%d\n",
		schedule.ID, schedule.Enabled, schedule.NextRunLocalDate, schedule.Revision)
	return nil
}

func reportRunNowCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports run-now", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scheduleID := fs.String("schedule", "", "schedule UUID")
	manualKey := fs.String("idempotency-key", "", "caller-supplied deterministic key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*scheduleID) == "" || strings.TrimSpace(*manualKey) == "" {
		return fmt.Errorf("usage: moonlight-cloud business-reports run-now --schedule <uuid> --idempotency-key <key>")
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runReportNow(context.Background(), env.service, env.runner, stdout,
		strings.TrimSpace(*scheduleID), strings.TrimSpace(*manualKey))
}

// runService is the narrow run boundary for CLI.
type runService interface {
	RunNow(ctx context.Context, scheduleID, manualKey string) (string, bool, error)
	ListRuns(ctx context.Context, limit int32) ([]businessreports.Run, error)
	GetRunStatus(ctx context.Context, id string) (businessreports.RunDetail, error)
}

// runReportNow creates one manual run and wakes the worker.
func runReportNow(ctx context.Context, service runService, runner *businessreports.Runner, out io.Writer, scheduleID, manualKey string) error {
	id, created, err := service.RunNow(ctx, scheduleID, manualKey)
	if err != nil {
		fmt.Fprintf(out, "error=%s\n", err.Error())
		return err
	}
	runner.Notify()
	fmt.Fprintf(out, "run=%s created=%v\n", id, created)
	return nil
}

func reportRunsCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud business-reports runs list|status ...")
	}
	switch args[0] {
	case "list":
		return reportRunsListCmd(args[1:], stdout, stderr)
	case "status":
		return reportRunStatusCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud business-reports runs list|status ...")
	}
}

func reportRunsListCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports runs list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runRunsList(context.Background(), env.service, stdout)
}

// runRunsList prints safe run summaries.
func runRunsList(ctx context.Context, service runService, out io.Writer) error {
	runs, err := service.ListRuns(ctx, 20)
	if err != nil {
		return err
	}
	for _, run := range runs {
		fmt.Fprintf(out, "run=%s schedule=%s kind=%s period=%s status=%s attempts=%d error=%s\n",
			run.ID, run.ScheduleID, run.Kind,
			run.PeriodStart.UTC().Format("2006-01-02")+".."+run.PeriodEnd.UTC().Format("2006-01-02"),
			run.Status, run.AttemptCount, run.LastErrorCode)
	}
	fmt.Fprintf(out, "count=%d\n", len(runs))
	return nil
}

func reportRunStatusCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("business-reports runs status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "run UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("usage: moonlight-cloud business-reports runs status --id <uuid>")
	}
	env, err := openBusinessReportsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runRunStatus(context.Background(), env.service, stdout, strings.TrimSpace(*id))
}

// runRunStatus prints one run with masked per-delivery outcomes.
func runRunStatus(ctx context.Context, service runService, out io.Writer, id string) error {
	detail, err := service.GetRunStatus(ctx, id)
	if err != nil {
		fmt.Fprintf(out, "error=%s\n", err.Error())
		return err
	}
	enqueued, blocked := 0, 0
	for _, delivery := range detail.Deliveries {
		switch delivery.Status {
		case businessreports.DeliveryEnqueued:
			enqueued++
		case businessreports.DeliveryBlocked:
			blocked++
		}
		fmt.Fprintf(out, "delivery=%s recipient=%s locale=%s status=%s notification=%s error=%s\n",
			delivery.ID, maskRecipient(delivery.Recipient), delivery.Locale,
			delivery.Status, delivery.NotificationID, delivery.LastErrorCode)
	}
	fmt.Fprintf(out, "run=%s status=%s enqueued=%d blocked=%d error=%s\n",
		detail.Run.ID, detail.Run.Status, enqueued, blocked, detail.Run.LastErrorCode)
	return nil
}

// businessReportsEnv holds one CLI invocation's database pool and
// wired report services. The pool stays open until close runs.
type businessReportsEnv struct {
	pool    interface{ Close() }
	service *businessreports.Service
	runner  *businessreports.Runner
}

func (e *businessReportsEnv) close() {
	if e != nil && e.pool != nil {
		e.pool.Close()
	}
}

// openBusinessReportsEnv loads config, opens the database, and wires
// the report service over canonical reports plus Phase 7A enqueue.
func openBusinessReportsEnv(stderr io.Writer) (*businessReportsEnv, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	store := postgres.NewDevices(pool, cfg.DBQueryTimeout)
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	notificationService := notifications.NewService(store, store, logger)
	reportService := report.NewService(store, clock.System{}, cfg.StoreLocation)
	loc, err := businessreports.LoadCairo()
	if err != nil {
		pool.Close()
		return nil, err
	}
	service := businessreports.NewService(store, store, store, store,
		reportService, notificationService,
		businessreports.SystemClock{}, loc,
		cfg.BusinessReports.LeaseDuration, cfg.BusinessReports.BatchSize, logger)
	runner := businessreports.NewRunner(store, store, reportService, notificationService,
		businessreports.SystemClock{}, loc, cfg.BusinessReports.PollInterval, 25,
		"business-reports-cli", cfg.BusinessReports.LeaseDuration, logger)
	return &businessReportsEnv{pool: pool, service: service, runner: runner}, nil
}
