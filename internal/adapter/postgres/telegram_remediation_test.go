package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

func r1Envelopes() []string {
	return []string{`{}`, `{"ok":null}`, `{"ok":false}`, `{"result":{"message_id":42,"chat":{"id":111}}}`, `{"ok":"true"}`, `{"ok":false,"error_code":0,"description":"error"}`, `{"ok":false,"error_code":200,"description":"error"}`, `{"ok":false,"error_code":403}`, `{"ok":false,"error_code":null,"description":"error"}`, `{"ok":false,"error_code":"403","description":"error"}`, `{"ok":true}`, `{"ok":true,"result":{"message_id":42}}`, `{"ok":true,"result":{"chat":{"id":111}}}`, `{"ok":true,"result":{"message_id":42,"chat":{"id":null}}}`, `{"ok":true,"result":{"message_id":42,"chat":{"id":111}}} garbage`, `{"ok":true,"result":{"message_id":42,"chat":{"id":111}}} {}`, `{"ok":true,`, strings.Repeat("x", (1<<20)+1), `{"ok":true,"ok":false,"error_code":429,"description":"retry"}`, `{"ok":true,"error_code":null,"result":{"message_id":42,"chat":{"id":111}}}`}
}

const r1Success = `{"ok":true,"result":{"message_id":42,"chat":{"id":111}},"future_field":{}}`

func TestTelegramR1RecipientCLI(t *testing.T) {
	binary := os.Getenv("MOONLIGHT_TELEGRAM_RUNTIME_BINARY")
	if binary == "" {
		t.Skip("MOONLIGHT_TELEGRAM_RUNTIME_BINARY unset: built CLI proof not run")
	}
	env := openSaleEnv(t)
	maximum := "@" + strings.Repeat("a", 32)
	for _, surface := range []string{"operations", "business-reports"} {
		for _, valid := range []bool{true, false} {
			address, label := maximum, surface+"-maximum"
			if !valid {
				address += "a"
				label = surface + "-oversized"
			}
			cmd := exec.Command(binary, surface, "recipients", "add", "--label", label, "--provider", "telegram-main", "--recipient", address, "--locale", "en")
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "ENVIRONMENT=development", "DATABASE_URL=" + env.pool.Config().ConnString(), "REPORTING_API_TOKEN=runtime-report-token-test-only"}
			output, err := cmd.CombinedOutput()
			if (err == nil) != valid {
				t.Fatalf("%s valid=%v: %v", surface, valid, err)
			}
			if bytes.Contains(output, []byte(address)) {
				t.Fatal("CLI exposed full recipient")
			}
			table := "operational_alert_recipients"
			if surface == "business-reports" {
				table = "business_report_recipients"
			}
			var count int
			if err := env.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE label=$1 AND recipient=$2", label, address).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if valid {
				want = 1
			}
			if count != want {
				t.Fatalf("CLI persistence count=%d want=%d", count, want)
			}
		}
	}
}

func r1Enqueue(t *testing.T, env *saleEnv, key, recipient, body string) string {
	t.Helper()
	store := catalogStore(env)
	result, err := notifications.NewService(store, store, slog.New(slog.NewTextHandler(io.Discard, nil))).EnqueueTemplate(context.Background(), notifications.EnqueueTemplateRequest{ProviderKey: "telegram-main", IdempotencyKey: key, Recipient: recipient, TemplateKey: "operator_test_v1", Locale: "ar", Parameters: map[string]string{"body": body}})
	if err != nil || !result.Created {
		t.Fatalf("enqueue: %+v %v", result, err)
	}
	return result.ID
}

func r1State(t *testing.T, env *saleEnv, id string) (string, bool) {
	t.Helper()
	var status string
	var started bool
	if err := env.pool.QueryRow(context.Background(), `SELECT dispatch_status,send_started_at IS NOT NULL FROM notification_messages WHERE id=$1`, id).Scan(&status, &started); err != nil {
		t.Fatal(err)
	}
	return status, started
}

func r1Eligible(t *testing.T, env *saleEnv, id string) {
	t.Helper()
	r, err := env.pool.Exec(context.Background(), `UPDATE notification_messages SET next_attempt_at=now()-interval '1 hour',lease_until=CASE WHEN lease_until IS NULL THEN NULL ELSE now()-interval '1 hour' END WHERE id=$1`, id)
	if err != nil || r.RowsAffected() != 1 {
		t.Fatal("make eligible", err)
	}
}

func TestTelegramR1DurableNoResend(t *testing.T) {
	cases := r1Envelopes()
	cases = append(cases, r1Success, `{"ok":false,"error_code":429,"description":"retry","parameters":{"retry_after":1}}`, `{"ok":false,"error_code":403,"description":"blocked"}`)
	for index, body := range cases {
		t.Run(fmt.Sprintf("envelope-%02d", index), func(t *testing.T) {
			env := openSaleEnv(t)
			seedTelegramTemplateMapping(t, env)
			id := r1Enqueue(t, env, "r1-proof", "@operations", "private report body")
			var mu sync.Mutex
			calls := 0
			var wire string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				mu.Lock()
				calls++
				call := calls
				wire = string(raw)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				response := body
				if index == len(cases)-2 && call > 1 {
					response = r1Success
				}
				io.WriteString(w, response)
			}))
			defer server.Close()
			provider := telegramTestProvider(t, &botStub{server: server})
			store := catalogStore(env)
			registry := telegramTestRegistry(t, provider)
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			notifications.NewDispatcher(store, registry, notifications.SystemClock{}, "first", log).DrainForTest(context.Background())
			status, started := r1State(t, env, id)
			expected := "ambiguous"
			if index == len(cases)-3 {
				expected = "accepted"
			}
			if index == len(cases)-2 {
				expected = "retry"
			}
			if index == len(cases)-1 {
				expected = "blocked"
			}
			if status != expected {
				t.Fatalf("first=%s want=%s", status, expected)
			}
			if expected == "ambiguous" && !started {
				t.Fatal("ambiguous send-start evidence cleared")
			}
			mu.Lock()
			first := calls
			requestBody := wire
			mu.Unlock()
			if first != 1 || !strings.Contains(requestBody, "private report body") {
				t.Fatal("request receipt not proven")
			}
			r1Eligible(t, env, id)
			notifications.NewDispatcher(store, registry, notifications.SystemClock{}, "restarted", log).DrainForTest(context.Background())
			status, finalStarted := r1State(t, env, id)
			mu.Lock()
			final := calls
			mu.Unlock()
			wantCalls := 1
			if expected == "retry" {
				wantCalls = 2
				expected = "accepted"
			}
			if final != wantCalls || status != expected {
				t.Fatalf("after restart state=%s calls=%d", status, final)
			}
			if expected == "ambiguous" && !finalStarted {
				t.Fatal("restart lost send-start evidence")
			}
			t.Logf("first=%s send_started=%v first_calls=%d restart=%s final_calls=%d", map[bool]string{true: "retry", false: expected}[wantCalls == 2], started, first, status, final)
		})
	}
}

// Explicit opt-in: this test launches the built candidate binary, then stops
// and starts a distinct OS process against the same durable database.
func TestTelegramR1ProcessRestart(t *testing.T) {
	binary := os.Getenv("MOONLIGHT_TELEGRAM_RUNTIME_BINARY")
	if binary == "" {
		t.Skip("MOONLIGHT_TELEGRAM_RUNTIME_BINARY unset: process runtime proof requires built candidate")
	}
	env := openSaleEnv(t)
	seedTelegramTemplateMapping(t, env)
	responses := r1Envelopes()[:4]
	ids := []string{}
	for i := range responses {
		ids = append(ids, r1Enqueue(t, env, fmt.Sprintf("runtime-%d", i), "@operations", fmt.Sprintf("runtime-body-%d", i)))
	}
	var mu sync.Mutex
	counts := make([]int, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		i := -1
		fmt.Sscanf(request.Text, "runtime-body-%d", &i)
		if i < 0 || i >= 4 {
			http.Error(w, "unknown test", 400)
			return
		}
		mu.Lock()
		counts[i]++
		mu.Unlock()
		io.WriteString(w, responses[i])
	}))
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "fake-telegram-root.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	start := func() (*exec.Cmd, *bytes.Buffer) {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		cmd := exec.Command(binary, "serve")
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "ENVIRONMENT=development", "DATABASE_URL=" + env.pool.Config().ConnString(), "HTTP_ADDR=" + address, "REPORTING_API_TOKEN=runtime-report-token-test-only", "SSL_CERT_FILE=" + certificate, "NOTIFICATIONS_TELEGRAM_ENABLED=true", "NOTIFICATIONS_TELEGRAM_PROVIDER_KEY=telegram-main", "NOTIFICATIONS_TELEGRAM_BOT_TOKEN=123456:AAA-runtime-only", "NOTIFICATIONS_TELEGRAM_BASE_URL=" + server.URL}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		return cmd, &output
	}
	stop := func(cmd *exec.Cmd, output *bytes.Buffer) {
		t.Helper()
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runtime stopped abnormally: %v", err)
			}
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Fatal("runtime shutdown timed out")
		}
		if strings.Contains(output.String(), "123456:AAA-runtime-only") || strings.Contains(output.String(), "runtime-body-") || strings.Contains(output.String(), server.URL) {
			t.Fatal("runtime logs leaked protected provider data")
		}
	}
	first, output := start()
	deadline := time.Now().Add(20 * time.Second)
	complete := false
	for time.Now().Before(deadline) {
		complete = true
		for _, id := range ids {
			status, _ := r1State(t, env, id)
			if status != "ambiguous" {
				complete = false
			}
		}
		if complete {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !complete {
		stop(first, output)
		t.Fatal("runtime never reached ambiguous")
	}
	stop(first, output)
	for _, id := range ids {
		status, started := r1State(t, env, id)
		if status != "ambiguous" || !started {
			t.Fatal("runtime evidence not retained")
		}
		r1Eligible(t, env, id)
	}
	mu.Lock()
	for _, n := range counts {
		if n != 1 {
			mu.Unlock()
			t.Fatal("first process must send once")
		}
	}
	mu.Unlock()
	second, secondOutput := start()
	time.Sleep(1500 * time.Millisecond)
	stop(second, secondOutput)
	mu.Lock()
	defer mu.Unlock()
	for i, id := range ids {
		status, started := r1State(t, env, id)
		if counts[i] != 1 || status != "ambiguous" || !started {
			t.Fatalf("process restart resent/lost evidence: %d %s %v", counts[i], status, started)
		}
		t.Logf("response=%d first=ambiguous started=true restart=ambiguous first_calls=1 final_calls=1", i)
	}
}

func TestTelegramR1UsernamePersistence(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	seedTelegramTemplateMapping(t, env)
	username := "@" + strings.Repeat("a", 32)
	over := "@" + strings.Repeat("a", 33)
	reports := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	recipient, err := reports.AddRecipient(ctx, "max username", "telegram-main", username, "ar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reports.AddRecipient(ctx, "too long", "telegram-main", over, "ar"); err == nil {
		t.Fatal("report administration accepted max+1")
	}
	ops := catalogStore(env)
	alertRecipient, err := ops.CreateOpsRecipient(ctx, ids.System{}.New(), "max username", "telegram-main", username, "en", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ops.CreateOpsRecipient(ctx, ids.System{}.New(), "too long", "telegram-main", over, "en", time.Now()); err == nil {
		t.Fatal("operations administration accepted max+1")
	}
	id := r1Enqueue(t, env, "max-username", username, "unchanged body")
	var stored string
	if err = env.pool.QueryRow(ctx, `SELECT recipient FROM notification_messages WHERE id=$1`, id).Scan(&stored); err != nil || stored != username {
		t.Fatal("queue truncated username", err)
	}
	for _, row := range []struct{ table, id string }{{"business_report_recipients", recipient.ID}, {"operational_alert_recipients", alertRecipient.ID}} {
		if err = env.pool.QueryRow(ctx, `SELECT recipient FROM `+row.table+` WHERE id=$1`, row.id).Scan(&stored); err != nil || stored != username {
			t.Fatal("administration changed username", err)
		}
	}
	if notifications.ValidateRecipient(over) == nil {
		t.Fatal("generic gate accepted max+1")
	}
	stub := newBotStub(t)
	notifications.NewDispatcher(catalogStore(env), telegramTestRegistry(t, telegramTestProvider(t, stub)), notifications.SystemClock{}, "max-username", slog.Default()).DrainForTest(ctx)
	status, _ := r1State(t, env, id)
	if status != "accepted" || stub.count() != 1 {
		t.Fatal("max username not dispatched")
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !strings.Contains(stub.bodies[0], username) {
		t.Fatal("wire username truncated")
	}
	// Generic administration does not decide provider-specific acceptance.
	if notifications.ValidateWhatsAppRecipient(username) == nil {
		t.Fatal("WhatsApp acceptance broadened")
	}
}
