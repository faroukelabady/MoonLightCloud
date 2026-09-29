package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openOpsStore(t *testing.T) (Devices, *pgxpool.Pool) {
	t.Helper()
	pool, _ := openTestRepo(t)
	return NewDevices(pool, 5*time.Second), pool
}

func opsService(store Devices) *operations.Service {
	return operations.NewService(store, ids.System{}.New, time.Now)
}

func opsSeedDevice(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	h, err := auth.NewHasher(testPepperBytes)
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(NewDevices(pool, 5*time.Second), h, "", 1, clock.System{}, ids.System{})
	prov, err := svc.Create(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return prov.Device.ID
}

func testOpsLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func seedOpsMapping(t *testing.T, store Devices) {
	t.Helper()
	ctx := context.Background()
	for _, locale := range []string{"ar", "en"} {
		for _, tpl := range []string{operations.TemplateOpen, operations.TemplateResolved} {
			if err := store.UpsertTemplateMapping(ctx, notifications.TemplateMapping{
				ProviderKey: "whatsapp-main", TemplateKey: tpl, Locale: locale,
				ExternalTemplateName: "ext_" + tpl, ExternalLanguageCode: locale,
				ParameterNames: []string{operations.ParamAlertBody}, Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func opsSeedRecipient(t *testing.T, store Devices, locale string) operations.Recipient {
	t.Helper()
	rec, err := store.CreateOpsRecipient(context.Background(), ids.System{}.New(),
		"ops-"+locale, "whatsapp-main", "201012345678", locale, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// Recipient CRUD masks nothing at rest but disables cleanly.
func TestOpsRecipients(t *testing.T) {
	store, _ := openOpsStore(t)
	ctx := context.Background()
	rec := opsSeedRecipient(t, store, "ar")
	all, err := store.ListOpsRecipients(ctx)
	if err != nil || len(all) != 1 || all[0].Recipient != "201012345678" {
		t.Fatalf("list: %+v %v", all, err)
	}
	enabled, err := store.ListEnabledOpsRecipients(ctx)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("enabled: %+v %v", enabled, err)
	}
	ok, err := store.DisableOpsRecipient(ctx, rec.ID, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("disable: %v %v", ok, err)
	}
	enabled, _ = store.ListEnabledOpsRecipients(ctx)
	if len(enabled) != 0 {
		t.Fatal("disabled excluded")
	}
}

// Stateful open converges across concurrent detectors; recurrence bumps episode.
func TestOpsStatefulDedupeRace(t *testing.T) {
	store, _ := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	const n = 12
	var wg sync.WaitGroup
	type res struct {
		id      string
		created bool
		err     error
	}
	out := make(chan res, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, created, err := svc.OpenStateful(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, "dev-1", "")
			if err != nil {
				out <- res{"", false, err}
				return
			}
			out <- res{in.ID, created, nil}
		}()
	}
	wg.Wait()
	close(out)
	seen := map[string]int{}
	created := 0
	for r := range out {
		if r.err != nil {
			t.Fatal(r.err)
		}
		seen[r.id]++
		if r.created {
			created++
		}
	}
	if len(seen) != 1 || created != 1 {
		t.Fatalf("one active incident: %v created=%d", seen, created)
	}
	// Resolve then recur: new episode, old row immutable.
	var id string
	for k := range seen {
		id = k
	}
	resolved, err := svc.Resolve(ctx, id, operations.ResolutionReconnect)
	if err != nil || resolved.State != "resolved" {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	second, recreated, err := svc.OpenStateful(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, "dev-1", "")
	if err != nil || !recreated || second.ID == id || second.Episode != 2 {
		t.Fatalf("recurrence: %+v %v %v", second, created, err)
	}
}

// Event dedupe is all-time: resolve never recreates.
func TestOpsEventDedupe(t *testing.T) {
	store, _ := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	first, created, err := svc.OpenEvent(ctx, operations.RuleDeviceSyncFailed, operations.SubjectSyncCommand, "cmd-1", "sync-command:cmd-1")
	if err != nil || !created {
		t.Fatalf("open: %+v %v %v", first, created, err)
	}
	if _, err := svc.Resolve(ctx, first.ID, operations.ResolutionOperator); err != nil {
		t.Fatal(err)
	}
	second, created, err := svc.OpenEvent(ctx, operations.RuleDeviceSyncFailed, operations.SubjectSyncCommand, "cmd-1", "sync-command:cmd-1")
	if err != nil || created || second.ID != first.ID || second.State != "resolved" {
		t.Fatalf("no recreate: %+v %v %v", second, created, err)
	}
}

// Acknowledge race converges; ack never resolves.
func TestOpsAcknowledgeRace(t *testing.T) {
	store, _ := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	opened, _, err := svc.OpenStateful(ctx, operations.RuleReportStale, operations.SubjectReportRun, "run-1", "")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := svc.Acknowledge(ctx, opened.ID)
			if err != nil || got.State != "acknowledged" {
				errs <- fmt.Errorf("ack: %+v %v", got, err)
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	final, _ := svc.Get(ctx, opened.ID)
	if final.State != "acknowledged" || final.AcknowledgedAt == nil {
		t.Fatalf("durable ack: %+v", final)
	}
}

// Resolve race: one winner, coherent final state.
func TestOpsResolveRace(t *testing.T) {
	store, _ := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	opened, _, err := svc.OpenStateful(ctx, operations.RuleReportStale, operations.SubjectReportRun, "run-9", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Resolve(ctx, opened.ID, operations.ResolutionOperator)
		}()
	}
	wg.Wait()
	final, _ := svc.Get(ctx, opened.ID)
	if final.State != "resolved" || final.ResolutionCode == nil {
		t.Fatalf("coherent resolved: %+v", final)
	}
}

// Recovery uniqueness: concurrent creates converge on one action.
func TestOpsRecoveryUniqueness(t *testing.T) {
	store, _ := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	opened, _, err := svc.OpenStateful(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, "dev-r", "")
	if err != nil {
		t.Fatal(err)
	}
	const n = 10
	var wg sync.WaitGroup
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, _, err := store.CreateRecovery(ctx, opened.ID, operations.ReconnectIdempotencyKey(opened.ID), time.Now().UTC())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- rec.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("one recovery action: %v", seen)
	}
}

// Alert crash adoption: same idempotency key + same semantics adopts the
// same notification across a simulated crash (no second row, no resend).
func TestOpsAlertCrashAdoption(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	seedOpsMapping(t, store)
	_ = pool
	notify := newOpsNotifier(store)
	incidentID := ids.System{}.New()
	deliveryID := ids.System{}.New()
	key := operations.AlertIdempotencyKey(incidentID, operations.EventOpened, deliveryID)
	first, err := notify.EnqueueTemplate(ctx, opsAlertIntent("201012345678", key))
	if err != nil {
		t.Fatal(err)
	}
	// Crash before notification_id persisted: retry same key + semantics.
	second, err := notify.EnqueueTemplate(ctx, opsAlertIntent("201012345678", key))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Created && second.ID != first.ID {
		t.Fatalf("adopt same notification: %+v vs %+v", first, second)
	}
	if second.Created || second.ID != first.ID {
		t.Fatalf("one notification row: %+v vs %+v", first, second)
	}
}

func newOpsNotifier(store Devices) *opsNotifier {
	return &opsNotifier{store: store}
}

type opsNotifier struct {
	store Devices
}

func (n *opsNotifier) EnqueueTemplate(ctx context.Context, req notifications.EnqueueTemplateRequest) (notifications.EnqueueResult, error) {
	svc := notifications.NewService(n.store, n.store, testOpsLogger())
	return svc.EnqueueTemplate(ctx, req)
}

func opsAlertIntent(recipient, key string) notifications.EnqueueTemplateRequest {
	return notifications.EnqueueTemplateRequest{
		ProviderKey: "whatsapp-main", IdempotencyKey: key, Recipient: recipient,
		TemplateKey: operations.TemplateOpen, Locale: "ar",
		Parameters: map[string]string{operations.ParamAlertBody: "test body"},
	}
}
