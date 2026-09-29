package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// operationsCmd is the operator surface for Phase 7D: alert recipients
// and incident inspection. Recipients print masked; incident output
// carries bounded machine summaries only.
func operationsCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud operations recipients|incidents ...")
	}
	switch args[0] {
	case "recipients":
		return opsRecipientsCmd(args[1:], stdout, stderr)
	case "incidents":
		return opsIncidentsCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud operations recipients|incidents ...")
	}
}

type opsEnv struct {
	store   postgres.Devices
	service *operations.Service
	reader  *operations.OpsReader
	close   func()
}

func openOpsEnv(stderr io.Writer) (*opsEnv, error) {
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
	_ = logger
	service := operations.NewService(store, ids.System{}.New, nil)
	reader := operations.NewOpsReader(store, service, operations.NewMetrics())
	return &opsEnv{store: store, service: service, reader: reader, close: pool.Close}, nil
}

func opsRecipientsCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud operations recipients add|list|disable ...")
	}
	switch args[0] {
	case "add":
		return opsRecipientAddCmd(args[1:], stdout, stderr)
	case "list":
		return opsRecipientListCmd(args[1:], stdout, stderr)
	case "disable":
		return opsRecipientDisableCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud operations recipients add|list|disable ...")
	}
}

func opsRecipientAddCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("operations recipients add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	label := fs.String("label", "", "operator label")
	providerKey := fs.String("provider", "", "notification provider key")
	recipient := fs.String("recipient", "", "canonical international recipient")
	locale := fs.String("locale", "", "ar|en")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*label) == "" || strings.TrimSpace(*providerKey) == "" ||
		strings.TrimSpace(*recipient) == "" || (*locale != "ar" && *locale != "en") {
		return fmt.Errorf("usage: moonlight-cloud operations recipients add --label <label> --provider <key> --recipient <addr> --locale <ar|en>")
	}
	if err := operations.ValidateLabel(strings.TrimSpace(*label)); err != nil {
		return err
	}
	env, err := openOpsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	rec, err := env.store.CreateOpsRecipient(context.Background(), ids.System{}.New(),
		strings.TrimSpace(*label), strings.TrimSpace(*providerKey),
		strings.TrimSpace(*recipient), *locale, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "id=%s label=%s provider=%s locale=%s enabled=%v recipient=%s\n",
		rec.ID, rec.Label, rec.ProviderKey, rec.Locale, rec.Enabled, maskRecipient(rec.Recipient))
	return nil
}

func opsRecipientListCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("operations recipients list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := openOpsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	recipients, err := env.store.ListOpsRecipients(context.Background())
	if err != nil {
		return err
	}
	for _, r := range recipients {
		fmt.Fprintf(stdout, "id=%s label=%s provider=%s locale=%s enabled=%v recipient=%s\n",
			r.ID, r.Label, r.ProviderKey, r.Locale, r.Enabled, maskRecipient(r.Recipient))
	}
	fmt.Fprintf(stdout, "count=%d\n", len(recipients))
	return nil
}

func opsRecipientDisableCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("operations recipients disable", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "recipient id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("usage: moonlight-cloud operations recipients disable --id <uuid>")
	}
	env, err := openOpsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	ok, err := env.store.DisableOpsRecipient(context.Background(), strings.TrimSpace(*id), time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "disabled=%v\n", ok)
	return nil
}

func opsIncidentsCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud operations incidents list|status ...")
	}
	switch args[0] {
	case "list":
		return opsIncidentsListCmd(args[1:], stdout, stderr)
	case "status":
		return opsIncidentStatusCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud operations incidents list|status ...")
	}
}

func opsIncidentsListCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("operations incidents list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "open|acknowledged|resolved")
	rule := fs.String("rule", "", "rule key filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := openOpsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	rows, next, err := env.service.List(context.Background(), strings.TrimSpace(*state), "", strings.TrimSpace(*rule), "", 50)
	if err != nil {
		return err
	}
	for _, in := range rows {
		fmt.Fprintf(stdout, "id=%s rule=%s subject=%s:%s severity=%s state=%s episode=%d opened=%s\n",
			in.ID, in.Rule, in.SubjectType, in.SubjectID, in.Severity, in.State,
			in.Episode, in.OpenedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
	fmt.Fprintf(stdout, "count=%d next=%s\n", len(rows), next)
	return nil
}

func opsIncidentStatusCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("operations incidents status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "incident id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("usage: moonlight-cloud operations incidents status --id <uuid>")
	}
	env, err := openOpsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	ctx := context.Background()
	incident, err := env.service.Get(ctx, strings.TrimSpace(*id))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "id=%s rule=%s subject=%s:%s severity=%s state=%s episode=%d\n",
		incident.ID, incident.Rule, incident.SubjectType, incident.SubjectID,
		incident.Severity, incident.State, incident.Episode)
	deliveries, err := env.reader.Deliveries(ctx, incident.ID)
	if err != nil {
		return err
	}
	for _, dl := range deliveries {
		fmt.Fprintf(stdout, "delivery=%s event=%s status=%s error=%v notification=%v\n",
			dl.ID, dl.Event, dl.Status, dl.LastErrorCode != nil, dl.NotificationID != nil)
	}
	recoveries, err := env.reader.Recoveries(ctx, incident.ID)
	if err != nil {
		return err
	}
	for _, rc := range recoveries {
		fmt.Fprintf(stdout, "recovery=%s action=%s state=%s result=%v target=%v\n",
			rc.ID, rc.ActionType, rc.State, rc.ResultCode != nil, rc.TargetEntityID != nil)
	}
	return nil
}
