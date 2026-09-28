package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/whatsapp"
)

// newNotificationRegistry builds the provider registry from runtime
// configuration. Disabled WhatsApp registers nothing. Construction
// performs no network I/O, so Cloud starts even when Meta is
// unreachable.
func newNotificationRegistry(cfg config.Config) (*notifications.Registry, error) {
	registry := notifications.NewRegistry()
	if !cfg.WhatsAppNotifications.Enabled {
		return registry, nil
	}
	provider, err := whatsapp.NewProvider(cfg.WhatsAppNotifications, nil)
	if err != nil {
		return nil, err
	}
	if err := registry.Register(provider.Key(), provider); err != nil {
		return nil, err
	}
	return registry, nil
}

// notificationsCmd is the narrow operator surface: template-map
// set|list, enqueue, status. No bulk send, no marketing tooling.
func notificationsCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud notifications template-map|enqueue|status ...")
	}
	switch args[0] {
	case "template-map":
		return notificationTemplateMapCmd(args[1:], stdout, stderr)
	case "enqueue":
		return notificationEnqueueCmd(args[1:], stdout, stderr)
	case "status":
		return notificationStatusCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud notifications template-map|enqueue|status ...")
	}
}

func notificationTemplateMapCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud notifications template-map set|list ...")
	}
	switch args[0] {
	case "set":
		return notificationTemplateMapSetCmd(args[1:], stdout, stderr)
	case "list":
		return notificationTemplateMapListCmd(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("usage: moonlight-cloud notifications template-map set|list ...")
	}
}

func notificationTemplateMapSetCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notifications template-map set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerKey := fs.String("provider", "", "registered provider key")
	templateKey := fs.String("template", "", "logical template key")
	locale := fs.String("locale", "", "locale tag")
	externalName := fs.String("external-name", "", "approved external template name")
	language := fs.String("language", "", "external language code")
	params := fs.String("params", "", "comma-separated ordered parameter names")
	enabled := fs.Bool("enabled", true, "mapping enabled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*providerKey) == "" || strings.TrimSpace(*templateKey) == "" ||
		strings.TrimSpace(*locale) == "" || strings.TrimSpace(*externalName) == "" ||
		strings.TrimSpace(*language) == "" {
		return fmt.Errorf("usage: moonlight-cloud notifications template-map set --provider <key> --template <key> --locale <tag> --external-name <name> --language <code> [--params a,b] [--enabled=false]")
	}
	env, err := openNotificationsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runTemplateMapSet(context.Background(), env.store, stdout,
		strings.TrimSpace(*providerKey), strings.TrimSpace(*templateKey), strings.TrimSpace(*locale),
		strings.TrimSpace(*externalName), strings.TrimSpace(*language), *params, *enabled)
}

func notificationTemplateMapListCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notifications template-map list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerKey := fs.String("provider", "", "registered provider key (empty for all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := openNotificationsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runTemplateMapList(context.Background(), env.store, stdout, strings.TrimSpace(*providerKey))
}

// templateMappingStore is the narrow durable boundary for mapping CLI.
type templateMappingStore interface {
	UpsertTemplateMapping(ctx context.Context, mapping notifications.TemplateMapping) error
	ListTemplateMappings(ctx context.Context, providerKey string) ([]notifications.TemplateMapping, error)
}

// runTemplateMapSet upserts one durable mapping and prints the safe
// summary (logical key, locale, external name, language, parameter
// names, enabled). No credentials involved.
func runTemplateMapSet(ctx context.Context, store templateMappingStore, out io.Writer, providerKey, templateKey, locale, externalName, language, params string, enabled bool) error {
	var names []string
	if strings.TrimSpace(params) != "" {
		for _, name := range strings.Split(params, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				return fmt.Errorf("invalid empty parameter name")
			}
			names = append(names, name)
		}
	}
	mapping := notifications.TemplateMapping{
		ProviderKey: providerKey, TemplateKey: templateKey, Locale: locale,
		ExternalTemplateName: externalName, ExternalLanguageCode: language,
		ParameterNames: names, Enabled: enabled,
	}
	if err := store.UpsertTemplateMapping(ctx, mapping); err != nil {
		fmt.Fprintf(out, "provider=%s template=%s locale=%s error=%s\n", providerKey, templateKey, locale, err.Error())
		return err
	}
	fmt.Fprintf(out, "provider=%s template=%s locale=%s external=%s language=%s params=%s enabled=%v\n",
		providerKey, templateKey, locale, externalName, language, strings.Join(names, ","), enabled)
	return nil
}

// runTemplateMapList prints durable mappings without secrets.
func runTemplateMapList(ctx context.Context, store templateMappingStore, out io.Writer, providerKey string) error {
	mappings, err := store.ListTemplateMappings(ctx, providerKey)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		fmt.Fprintf(out, "provider=%s template=%s locale=%s external=%s language=%s params=%s enabled=%v\n",
			mapping.ProviderKey, mapping.TemplateKey, mapping.Locale,
			mapping.ExternalTemplateName, mapping.ExternalLanguageCode,
			strings.Join(mapping.ParameterNames, ","), mapping.Enabled)
	}
	fmt.Fprintf(out, "count=%d\n", len(mappings))
	return nil
}

// paramFlags collects repeatable --param name=value pairs.
type paramFlags map[string]string

func (p paramFlags) String() string { return fmt.Sprint(map[string]string(p)) }

func (p paramFlags) Set(value string) error {
	name, val, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid --param %q: want name=value", value)
	}
	p[strings.TrimSpace(name)] = val
	return nil
}

func notificationEnqueueCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notifications enqueue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerKey := fs.String("provider", "", "registered provider key")
	recipient := fs.String("to", "", "canonical international recipient")
	templateKey := fs.String("template", "", "logical template key")
	locale := fs.String("locale", "", "locale tag")
	idempotencyKey := fs.String("idempotency-key", "", "deterministic caller key")
	params := paramFlags{}
	fs.Var(params, "param", "template parameter name=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*providerKey) == "" || strings.TrimSpace(*recipient) == "" ||
		strings.TrimSpace(*templateKey) == "" || strings.TrimSpace(*locale) == "" ||
		strings.TrimSpace(*idempotencyKey) == "" {
		return fmt.Errorf("usage: moonlight-cloud notifications enqueue --provider <key> --to <recipient> --template <key> --locale <tag> --idempotency-key <key> [--param name=value ...]")
	}
	env, err := openNotificationsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runNotificationEnqueue(context.Background(), env.notificationService, stdout,
		strings.TrimSpace(*providerKey), strings.TrimSpace(*recipient), strings.TrimSpace(*templateKey),
		strings.TrimSpace(*locale), strings.TrimSpace(*idempotencyKey), map[string]string(params))
}

// runNotificationEnqueue creates durable work and prints the safe
// summary. The recipient is never echoed: the operator supplied it,
// and logs must not retain it.
func runNotificationEnqueue(ctx context.Context, service *notifications.Service, out io.Writer, providerKey, recipient, templateKey, locale, idempotencyKey string, params map[string]string) error {
	result, err := service.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: providerKey, IdempotencyKey: idempotencyKey,
		Recipient: recipient, TemplateKey: templateKey, Locale: locale, Parameters: params,
	})
	if err != nil {
		fmt.Fprintf(out, "provider=%s template=%s locale=%s created=false error=%s\n",
			providerKey, templateKey, locale, err.Error())
		return err
	}
	fmt.Fprintf(out, "id=%s provider=%s template=%s locale=%s created=%v\n",
		result.ID, result.ProviderKey, result.TemplateKey, result.Locale, result.Created)
	return nil
}

func notificationStatusCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notifications status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "notification UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("usage: moonlight-cloud notifications status --id <uuid>")
	}
	env, err := openNotificationsEnv(stderr)
	if err != nil {
		return err
	}
	defer env.close()
	return runNotificationStatus(context.Background(), env.store, stdout, strings.TrimSpace(*id))
}

// notificationStatusStore is the narrow durable boundary for status CLI.
type notificationStatusStore interface {
	GetNotificationStatus(ctx context.Context, id string) (postgres.NotificationStatus, error)
}

// runNotificationStatus prints safe operator state: no recipient, no
// parameter contents, no credentials.
func runNotificationStatus(ctx context.Context, store notificationStatusStore, out io.Writer, id string) error {
	status, err := store.GetNotificationStatus(ctx, id)
	if err != nil {
		fmt.Fprintf(out, "id=%s error=%s\n", id, err.Error())
		return err
	}
	fmt.Fprintf(out, "id=%s provider=%s template=%s locale=%s dispatch=%s delivery=%s attempts=%d message_id=%s error=%s created=%s updated=%s\n",
		status.ID, status.ProviderKey, status.TemplateKey, status.Locale,
		status.Dispatch, status.Delivery, status.AttemptCount,
		status.ProviderMessageID, status.LastErrorCode,
		status.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		status.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	return nil
}

// notificationsEnv holds one CLI invocation's database pool and wired
// notification services. The pool stays open until close runs.
type notificationsEnv struct {
	pool                interface{ Close() }
	store               postgres.Devices
	notificationService *notifications.Service
}

func (e *notificationsEnv) close() {
	if e != nil && e.pool != nil {
		e.pool.Close()
	}
}

// openNotificationsEnv loads config, opens the database, and wires the
// notification registry plus enqueue service.
func openNotificationsEnv(stderr io.Writer) (*notificationsEnv, error) {
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
	return &notificationsEnv{
		pool:                pool,
		store:               store,
		notificationService: notifications.NewService(store, store, logger),
	}, nil
}
