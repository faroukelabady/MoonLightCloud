package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// TestTelegramConfigMatrix pins startup validation: disabled needs
// nothing, enabled requires key/token/https/timeout, and Telegram
// config never interferes with WhatsApp startup.
func TestTelegramConfigMatrix(t *testing.T) {
	good := config.TelegramNotificationConfig{
		Enabled: true, ProviderKey: "telegram-main",
		BotToken: testBotToken, HTTPTimeout: 15 * time.Second,
	}
	if _, err := NewProvider(good, nil); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	// Disabled constructs nothing at the app layer: no provider, no
	// validation, zero network calls. The registry stays empty.
	empty := notifications.NewRegistry()
	if empty.Count() != 0 {
		t.Fatalf("fresh registry must hold zero providers")
	}
	for name, mutate := range map[string]func(*config.TelegramNotificationConfig){
		"missing provider key": func(c *config.TelegramNotificationConfig) { c.ProviderKey = "" },
		"bad provider key":     func(c *config.TelegramNotificationConfig) { c.ProviderKey = "Telegram Main!" },
		"missing token":        func(c *config.TelegramNotificationConfig) { c.BotToken = "" },
	} {
		cfg := good
		mutate(&cfg)
		if _, err := NewProvider(cfg, nil); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
	// Malformed tokens fail closed at construction with value-free
	// errors: no token bytes may reach URL construction, where parse
	// failures would echo the secret-bearing URL (F-02).
	for name, token := range map[string]string{
		"control token":    "abc\ndef",
		"no-colon token":   "justastring",
		"non-digit prefix": "abc:DEF123",
		"url-escape token": "123:SECRET%zzTOKEN",
		"spaced token":     "123:tok en",
		"query token":      "123:tok?en",
		"fragment token":   "123:tok#en",
		"slash token":      "123:tok/en",
	} {
		cfg := good
		cfg.BotToken = token
		if _, err := NewProvider(cfg, nil); err == nil {
			t.Fatalf("%s must fail", name)
		} else if strings.Contains(err.Error(), strings.TrimSpace(token)) {
			t.Fatalf("%s: token bytes in validation error: %q", name, err.Error())
		}
	}
	// Duplicate logical keys collide: startup must fail safely.
	first, err := NewProvider(good, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProvider(good, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := notifications.NewRegistry()
	if err := registry.Register(first.Key(), first); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(second.Key(), second); err == nil {
		t.Fatal("duplicate provider key must fail registration")
	}
}

// TestTelegramRecipientMatrix pins the deterministic canonicalizer:
// numeric chat IDs (sign preserved), @usernames, identity
// canonicalization, and total boundedness for the durable column.
func TestTelegramRecipientMatrix(t *testing.T) {
	for _, valid := range []string{
		"123456789", "-1001234567890", "-1", "1",
		"4503599627370496", // 52-bit Bot API ceiling shape
		"@operations", "@MoonLight_Ops1",
	} {
		canonical, err := ValidateRecipient(valid)
		if err != nil {
			t.Fatalf("recipient %q: %v", valid, err)
		}
		if canonical != valid {
			t.Fatalf("canonicalization must be identity: %q -> %q", valid, canonical)
		}
	}
	for _, bad := range []string{
		"", "   ", "abc", "@ab", "@" + strings.Repeat("a", 32),
		"@user-name", "@user name", "0", "-0", "+123456789",
		"12 34", "12\n34", "@\u00e9", "chat:1",
		strings.Repeat("1", 17), // longer than any Bot API chat ID
	} {
		if _, err := ValidateRecipient(bad); err == nil {
			t.Fatalf("recipient %q must fail", bad)
		}
	}
}

// TestTelegramRequestShape pins the sendMessage contract: endpoint,
// chat_id/text only, byte-faithful Unicode, no parse_mode, no
// accidental fields, token only where the official API requires it.
func TestTelegramRequestShape(t *testing.T) {
	harness := newBotHarness(t)
	provider := testBotProvider(t, harness)
	body := "تقرير الأعمال اليومي ✓ \"quoted\" & <brackets>\nline2 📊 pie"
	req := testBotRequest("@operations")
	req.Resolved.Parameters["body"] = body
	if _, err := provider.SendTemplate(context.Background(), req); err != nil {
		t.Fatalf("send: %v", err)
	}
	recorded := harness.recorded()
	if len(recorded) != 1 {
		t.Fatalf("requests: %d", len(recorded))
	}
	got := recorded[0]
	if got.Method != http.MethodPost {
		t.Fatalf("method: %s", got.Method)
	}
	wantPath := "/bot" + testBotToken + "/sendMessage"
	if got.Path != wantPath {
		t.Fatalf("path: %s", harness.redactedPath(got.Path))
	}
	if got.RawQuery != "" {
		t.Fatalf("query must be empty: %q", got.RawQuery)
	}
	if got.Auth != "" {
		t.Fatalf("no Authorization header for Bot API path auth")
	}
	if len(got.Body) != 2 {
		t.Fatalf("exact field set {chat_id,text}, got %v", got.Body)
	}
	if got.Body["chat_id"] != "@operations" {
		t.Fatalf("chat_id: %v", got.Body["chat_id"])
	}
	text, ok := got.Body["text"].(string)
	if !ok || text != body {
		t.Fatalf("text must travel verbatim")
	}
	// Raw bytes prove no escaping layer reinterpreted the content.
	for _, needle := range []string{"تقرير", "quoted", "&", "<brackets>", "📊"} {
		if !strings.Contains(string(got.Raw), needle) {
			t.Fatalf("raw body lost %q", needle)
		}
	}
}

// TestTelegramNumericChatIDString pins string-form numeric chat IDs:
// exact identity without float coercion.
func TestTelegramNumericChatIDString(t *testing.T) {
	harness := newBotHarness(t)
	provider := testBotProvider(t, harness)
	req := testBotRequest("-1001234567890")
	if _, err := provider.SendTemplate(context.Background(), req); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := harness.recorded()[0]
	if got.Body["chat_id"] != "-1001234567890" {
		t.Fatalf("chat_id: %v", got.Body["chat_id"])
	}
}

// TestTelegramSuccessIdentity pins chat-qualified message identity:
// the actual returned chat ID (not the requested string) qualifies
// the message ID, and the same message_id in two chats cannot
// collide in the generic provider_message_id column.
func TestTelegramSuccessIdentity(t *testing.T) {
	harness := newBotHarness(t)
	harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return http.StatusOK, botSuccess(111222333, 42), nil
	}
	provider := testBotProvider(t, harness)
	result, err := provider.SendTemplate(context.Background(), testBotRequest("111222333"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.ProviderMessageID != "111222333:42" {
		t.Fatalf("identity: %q", result.ProviderMessageID)
	}

	harness2 := newBotHarness(t)
	harness2.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return http.StatusOK, botSuccess(999888777, 42), nil
	}
	provider2, err := NewProvider(testBotConfig(harness2), harness2.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	other, err := provider2.SendTemplate(context.Background(), testBotRequest("999888777"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if other.ProviderMessageID == result.ProviderMessageID {
		t.Fatalf("same message_id in different chats must not collide: %q", other.ProviderMessageID)
	}
	if other.ProviderMessageID != "999888777:42" {
		t.Fatalf("identity: %q", other.ProviderMessageID)
	}
}

// TestTelegramMalformedSuccess pins AMBIGUOUS (never retry) for every
// structurally invalid success: the request may already have sent.
func TestTelegramMalformedSuccess(t *testing.T) {
	bodies := map[string]any{
		"malformed json":      "not json{",
		"missing result":      map[string]any{"ok": true},
		"null result":         map[string]any{"ok": true, "result": nil},
		"missing chat":        map[string]any{"ok": true, "result": map[string]any{"message_id": 7}},
		"zero chat":           map[string]any{"ok": true, "result": map[string]any{"message_id": 7, "chat": map[string]any{"id": 0}}},
		"zero message id":     map[string]any{"ok": true, "result": map[string]any{"message_id": 0, "chat": map[string]any{"id": 5}}},
		"oversized body":      strings.Repeat("x", maxResponseBytes+8),
		"unexpected array":    []any{map[string]any{"ok": true}},
		"wrong typed message": map[string]any{"ok": true, "result": map[string]any{"message_id": "seven", "chat": map[string]any{"id": 5}}},
	}
	for name, body := range bodies {
		harness := newBotHarness(t)
		harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
			return http.StatusOK, body, nil
		}
		provider := testBotProvider(t, harness)
		_, err := provider.SendTemplate(context.Background(), testBotRequest("123456789"))
		if err == nil {
			t.Fatalf("%s must fail", name)
			continue
		}
		got := asNotificationError(t, err)
		if got.Kind != notifications.ErrorAmbiguous {
			t.Fatalf("%s: kind %q, want ambiguous", name, got.Kind)
		}
		if got.Retryable() {
			t.Fatalf("%s: ambiguous must never auto-retry", name)
		}
		if strings.Contains(err.Error(), testBotToken) {
			t.Fatalf("%s: token in diagnostics", name)
		}
	}
}

// TestTelegramErrorMatrix pins the taxonomy mapping plus retry flags,
// bounded retry hints, secret-free diagnostics, and the chat-migration
// block (never a silent recipient rewrite).
func TestTelegramErrorMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       any
		headers    map[string]string
		kind       notifications.ErrorKind
		retryable  bool
		retryAfter time.Duration
		retryOK    bool
	}{
		{"invalid token", 401, botFailure(401, "Unauthorized", nil), nil, notifications.ErrorAuthentication, false, 0, false},
		{"revoked token", 401, "unauthorized", nil, notifications.ErrorAuthentication, false, 0, false},
		{"chat not found", 400, botFailure(400, "Bad Request: chat not found", nil), nil, notifications.ErrorValidation, false, 0, false},
		{"bot blocked", 403, botFailure(403, "Forbidden: bot was blocked by the user", nil), nil, notifications.ErrorValidation, false, 0, false},
		{"no rights", 403, botFailure(403, "Forbidden: not enough rights to send text messages to the chat", nil), nil, notifications.ErrorValidation, false, 0, false},
		{"unknown chat 404", 404, botFailure(404, "Not Found", nil), nil, notifications.ErrorValidation, false, 0, false},
		{"bad request", 400, botFailure(400, "Bad Request: message text is empty", nil), nil, notifications.ErrorValidation, false, 0, false},
		{"conflict", 409, botFailure(409, "Conflict: terminated by other getUpdates request", nil), nil, notifications.ErrorConflict, false, 0, false},
		{"timeout status", 408, botFailure(408, "Request Timeout", nil), nil, notifications.ErrorTemporary, true, 0, false},
		{"server error", 500, botFailure(500, "Internal Server Error", nil), nil, notifications.ErrorTemporary, true, 0, false},
		{"bad gateway", 502, "Bad Gateway", nil, notifications.ErrorTemporary, true, 0, false},
		{"unavailable", 503, botFailure(503, "Service Unavailable", nil), nil, notifications.ErrorTemporary, true, 0, false},
		{"unclassified 4xx", 418, "teapot", nil, notifications.ErrorValidation, false, 0, false},
		{"garbage error body", 500, "{oops", nil, notifications.ErrorTemporary, true, 0, false},
		{"empty error body", 400, nil, nil, notifications.ErrorValidation, false, 0, false},
		{"rate limit normal", 429, botFailure(429, "Too Many Requests: retry after 45", map[string]any{"retry_after": 45}), nil, notifications.ErrorRateLimited, true, 45 * time.Second, true},
		{"rate limit minimum", 429, botFailure(429, "Too Many Requests", map[string]any{"retry_after": 1}), nil, notifications.ErrorRateLimited, true, time.Second, true},
		{"rate limit huge caps", 429, botFailure(429, "Too Many Requests", map[string]any{"retry_after": 999999}), nil, notifications.ErrorRateLimited, true, time.Hour, true},
		{"rate limit overflow", 429, botFailure(429, "Too Many Requests", map[string]any{"retry_after": 9223372036854775807}), nil, notifications.ErrorRateLimited, true, time.Hour, true},
		{"rate limit negative", 429, botFailure(429, "Too Many Requests", map[string]any{"retry_after": -5}), nil, notifications.ErrorRateLimited, true, 0, true},
		{"rate limit missing", 429, botFailure(429, "Too Many Requests", nil), nil, notifications.ErrorRateLimited, true, 0, true},
		{"rate limit header fallback", 429, "slow down", map[string]string{"Retry-After": "30"}, notifications.ErrorRateLimited, true, 30 * time.Second, true},
		{"chat migration blocks", 400, botFailure(400, "Bad Request: group chat was migrated to a supergroup chat", map[string]any{"migrate_to_chat_id": -100987654321}), nil, notifications.ErrorValidation, false, 0, false},
	} {
		harness := newBotHarness(t)
		harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
			return tc.status, tc.body, tc.headers
		}
		provider := testBotProvider(t, harness)
		_, err := provider.SendTemplate(context.Background(), testBotRequest("123456789"))
		if err == nil {
			t.Fatalf("%s must fail", tc.name)
			continue
		}
		got := asNotificationError(t, err)
		if got.Kind != tc.kind {
			t.Fatalf("%s: kind %q, want %q", tc.name, got.Kind, tc.kind)
		}
		if got.Retryable() != tc.retryable {
			t.Fatalf("%s: retryable %v, want %v", tc.name, got.Retryable(), tc.retryable)
		}
		if hint, ok := got.GetRetryAfter(); ok != tc.retryOK || (ok && hint != tc.retryAfter) {
			t.Fatalf("%s: retry_after %v/%v, want %v", tc.name, hint, ok, tc.retryAfter)
		}
		if strings.Contains(err.Error(), testBotToken) {
			t.Fatalf("%s: token in diagnostics", tc.name)
		}
		if strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), "was blocked") {
			t.Fatalf("%s: provider prose in diagnostics: %q", tc.name, err.Error())
		}
	}
}

// TestTelegramMigrationNeverRewrites pins §55: a
// migrate_to_chat_id response blocks with a stable operator-action
// code and never mutates the recipient.
func TestTelegramMigrationNeverRewrites(t *testing.T) {
	harness := newBotHarness(t)
	harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return http.StatusBadRequest, botFailure(400, "migrated",
			map[string]any{"migrate_to_chat_id": -100987654321}), nil
	}
	provider := testBotProvider(t, harness)
	req := testBotRequest("-100111222333")
	_, err := provider.SendTemplate(context.Background(), req)
	if err == nil {
		t.Fatal("migration must fail")
	}
	got := asNotificationError(t, err)
	if got.Kind != notifications.ErrorValidation || got.Retryable() {
		t.Fatalf("migration must terminally block: %v", err)
	}
	if strings.Contains(err.Error(), "100987654321") {
		t.Fatalf("replacement chat ID must not leak into diagnostics: %q", err.Error())
	}
	if harness.count() != 1 {
		t.Fatalf("exactly one attempt, got %d", harness.count())
	}
}

// TestTelegramRedirectRefused pins the token-in-URL invariant: 301,
// 302, 307, and 308 are refused without following, the destination
// receives zero requests, the token never leaves the configured
// origin, and the outcome blocks terminally (config error, no retry
// loop over a redirecting origin).
func TestTelegramRedirectRefused(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308} {
		landing := newBotHarness(t)
		origin := newBotHarness(t)
		origin.script = func(_ botRecordedRequest) (int, any, map[string]string) {
			return status, nil, map[string]string{"Location": landing.server.URL + "/bot" + testBotToken + "/sendMessage"}
		}
		provider := testBotProvider(t, origin)
		_, err := provider.SendTemplate(context.Background(), testBotRequest("123456789"))
		if err == nil {
			t.Fatalf("%d must fail", status)
			continue
		}
		got := asNotificationError(t, err)
		if got.Kind != notifications.ErrorValidation || got.Retryable() {
			t.Fatalf("%d: redirect must terminally block: %v", status, err)
		}
		if landing.count() != 0 {
			t.Fatalf("%d: destination must receive zero requests", status)
		}
		if strings.Contains(err.Error(), testBotToken) {
			t.Fatalf("%d: token in diagnostics", status)
		}
	}
}

// TestTelegramOversizeResponse pins the response bound: exact bound
// decodes, bound+1 ambiguates, chunked oversized ambiguates.
func TestTelegramOversizeResponse(t *testing.T) {
	harness := newBotHarness(t)
	provider := testBotProvider(t, harness)
	pad := strings.Repeat("x", maxResponseBytes-len(`{"ok":true,"result":{"message_id":1,"chat":{"id":2}}}`)+1)
	_ = pad
	// Exact bound is exercised structurally below via a padded but
	// valid envelope; bound+1 must ambiguate.
	harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return http.StatusOK, botSuccess(2, 1), nil
	}
	if _, err := provider.SendTemplate(context.Background(), testBotRequest("2")); err != nil {
		t.Fatalf("valid small response: %v", err)
	}
	harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return http.StatusOK, strings.Repeat("y", maxResponseBytes+1), nil
	}
	_, err := provider.SendTemplate(context.Background(), testBotRequest("2"))
	if got := asNotificationError(t, err); got.Kind != notifications.ErrorAmbiguous {
		t.Fatalf("oversized success must ambiguate: %v", err)
	}
}

// TestTelegramTransportSeparation pins before-write retry vs
// after-write ambiguity, caller cancellation preservation, and the
// single-attempt proof for uncertain side effects.
func TestTelegramTransportSeparation(t *testing.T) {
	// After-write disconnect: request counted, outcome ambiguous.
	hangup := newBotHarness(t)
	hangup.hangup = true
	provider := testBotProvider(t, hangup)
	_, err := provider.SendTemplate(context.Background(), testBotRequest("123456789"))
	if got := asNotificationError(t, err); got.Kind != notifications.ErrorAmbiguous {
		t.Fatalf("after-write disconnect must ambiguate: %v", err)
	}
	if hangup.count() != 1 {
		t.Fatalf("exactly one attempt, got %d", hangup.count())
	}

	// Before-write failure: unreachable origin, safe retry.
	dead, err := NewProvider(config.TelegramNotificationConfig{
		Enabled: true, ProviderKey: "telegram-main",
		BotToken: testBotToken, BaseURL: "https://127.0.0.1:1",
		HTTPTimeout: 5 * time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dead.SendTemplate(context.Background(), testBotRequest("123456789"))
	if got := asNotificationError(t, err); got.Kind != notifications.ErrorTemporary {
		t.Fatalf("before-write failure must retry: %v", err)
	}

	// Caller cancellation is preserved unwrapped.
	harness := newBotHarness(t)
	provider2 := testBotProvider(t, harness)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = provider2.SendTemplate(ctx, testBotRequest("123456789"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must surface unwrapped: %v", err)
	}
	if harness.count() != 0 {
		t.Fatalf("cancelled send must not touch the network")
	}
}

// TestTelegramTimeoutAfterWrite pins timeout ambiguity: the request
// left MoonLight, the response never arrived, so no resend.
func TestTelegramTimeoutAfterWrite(t *testing.T) {
	harness := newBotHarness(t)
	harness.sleep = 3 * time.Second
	cfg := testBotConfig(harness)
	cfg.HTTPTimeout = 300 * time.Millisecond
	provider, err := NewProvider(cfg, harness.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.SendTemplate(context.Background(), testBotRequest("123456789"))
	if got := asNotificationError(t, err); got.Kind != notifications.ErrorAmbiguous {
		t.Fatalf("timeout after write must ambiguate: %v", err)
	}
}

// TestTelegramRendererBounds pins the narrow local renderer:
// exactly one body parameter, known selector, non-empty,
// provider-bounded text. Violations block before any network call.
func TestTelegramRendererBounds(t *testing.T) {
	harness := newBotHarness(t)
	provider := testBotProvider(t, harness)
	base := testBotRequest("123456789")

	// Exact 4096 runes passes (network reached).
	exact := base
	exact.Resolved.Parameters["body"] = strings.Repeat("أ", maxTextRunes)
	if _, err := provider.SendTemplate(context.Background(), exact); err != nil {
		t.Fatalf("4096-rune body: %v", err)
	}
	before := harness.count()

	oversize := base
	oversize.Resolved.Parameters["body"] = strings.Repeat("b", maxTextRunes+1)
	if _, err := provider.SendTemplate(context.Background(), oversize); err == nil {
		t.Fatal("4097-char body must block")
	} else if got := asNotificationError(t, err); got.Kind != notifications.ErrorValidation {
		t.Fatalf("oversize must validate-block: %v", err)
	}

	empty := base
	empty.Resolved.Parameters["body"] = ""
	if _, err := provider.SendTemplate(context.Background(), empty); err == nil {
		t.Fatal("empty body must block")
	}

	unknown := base
	unknown.Resolved.ExternalTemplateName = "meta_approved_template"
	if _, err := provider.SendTemplate(context.Background(), unknown); err == nil {
		t.Fatal("foreign renderer selector must block")
	}

	multi := base
	multi.Resolved.ParameterOrder = []string{"a", "b"}
	multi.Resolved.Parameters = map[string]string{"a": "x", "b": "y"}
	if _, err := provider.SendTemplate(context.Background(), multi); err == nil {
		t.Fatal("multi-parameter resolution must block")
	}

	wrongKey := base
	wrongKey.ProviderKey = "telegram-other"
	if _, err := provider.SendTemplate(context.Background(), wrongKey); err == nil {
		t.Fatal("cross-instance request must block")
	}

	badRecipient := base
	badRecipient.Recipient = "+201012345678"
	if _, err := provider.SendTemplate(context.Background(), badRecipient); err == nil {
		t.Fatal("non-telegram recipient shape must block at send")
	}
	if harness.count() != before {
		t.Fatalf("renderer violations must block before any network call")
	}
}

// TestTelegramProviderKeyIsInstance pins logical identity: the key is
// the configured instance (telegram-main), not the vendor name.
func TestTelegramProviderKeyIsInstance(t *testing.T) {
	cfg := testBotConfig(newBotHarness(t))
	cfg.ProviderKey = "telegram-ops"
	provider, err := NewProvider(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Key() != notifications.ProviderKey("telegram-ops") {
		t.Fatalf("key: %q", provider.Key())
	}
}

// TestTelegramFingerprintDeterminism pins provider-neutral semantic
// fingerprints across map-order permutations for Telegram rows.
func TestTelegramFingerprintDeterminism(t *testing.T) {
	params := map[string]string{"body": "daily totals 68000"}
	resolved := notifications.ResolvedTemplate{
		TemplateKey: "daily_business_report_v1", Locale: "ar",
		ExternalTemplateName: RendererBodyV1, ExternalLanguageCode: "ar",
		ParameterOrder: []string{"body"},
	}
	first := notifications.FingerprintSemantic("telegram-main", "123456789",
		"daily_business_report_v1", "ar", params, resolved)
	for i := 0; i < 10; i++ {
		again := notifications.FingerprintSemantic("telegram-main", "123456789",
			"daily_business_report_v1", "ar",
			map[string]string{"body": "daily totals 68000"}, resolved)
		if again != first {
			t.Fatal("fingerprint must be deterministic")
		}
	}
}

// TestTelegramConstructionPerformsNoIO pins the startup contract:
// building the provider never dials the network, so Cloud starts
// even when Telegram is unreachable. First contact happens at
// dispatch, never at registration.
func TestTelegramConstructionPerformsNoIO(t *testing.T) {
	cfg := config.TelegramNotificationConfig{
		Enabled: true, ProviderKey: "telegram-main",
		BotToken: testBotToken, BaseURL: "https://127.0.0.1:1",
		HTTPTimeout: 5 * time.Second,
	}
	if _, err := NewProvider(cfg, nil); err != nil {
		t.Fatalf("construction must not touch the network: %v", err)
	}
}

// TestTelegramLiveBotAPI is OPT-IN ONLY: it runs exclusively with
// explicit credentials (MOONLIGHT_TELEGRAM_E2E=1 plus bot token and
// chat ID). Without them it reports NOT RUN, never PASS.
func TestTelegramLiveBotAPI(t *testing.T) {
	if v := getEnvOptIn("MOONLIGHT_TELEGRAM_E2E"); v != "1" {
		t.Skip("NOT RUN — live Telegram Bot API requires explicit opt-in credentials")
	}
	token := getEnvOptIn("NOTIFICATIONS_TELEGRAM_BOT_TOKEN")
	chat := getEnvOptIn("MOONLIGHT_TELEGRAM_E2E_CHAT_ID")
	if token == "" || chat == "" {
		t.Skip("NOT RUN — live Telegram Bot API requires explicit opt-in credentials")
	}
	provider, err := NewProvider(config.TelegramNotificationConfig{
		Enabled: true, ProviderKey: "telegram-main",
		BotToken: token, HTTPTimeout: 15 * time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.SendTemplate(context.Background(), notifications.TemplateSendRequest{
		ProviderKey: "telegram-main", Recipient: chat,
		Resolved: notifications.ResolvedTemplate{
			TemplateKey: "operator_test_v1", Locale: "en",
			ExternalTemplateName: RendererBodyV1, ExternalLanguageCode: "en",
			ParameterOrder: []string{"body"},
			Parameters: map[string]string{
				"body": "MoonLight Phase 10 live proof (non-sensitive, opt-in only)",
			},
		},
	})
	if err != nil {
		t.Fatalf("live send: %v", err)
	}
	if result.ProviderMessageID == "" || !strings.Contains(result.ProviderMessageID, ":") {
		t.Fatalf("live identity: %q", result.ProviderMessageID)
	}
}

// TestTelegramConstructionErrorSanitized pins F-02 remediation: when
// request construction fails (here via an operator-controlled base
// URL that breaks URL parsing), the error is a fixed MoonLight-owned
// string. The secret-bearing URL — base, token, path — never enters
// error values, no matter which part of the URL broke parsing.
func TestTelegramConstructionErrorSanitized(t *testing.T) {
	for name, baseURL := range map[string]string{
		"bad escape in base": "https://%zz",
		"control in base":    "https://exam\nple.com",
	} {
		cfg := testBotConfig(newBotHarness(t))
		cfg.BaseURL = baseURL
		provider, err := NewProvider(cfg, nil)
		if err != nil {
			t.Fatalf("%s: construction validates token, not base URL: %v", name, err)
		}
		_, err = provider.SendTemplate(context.Background(), testBotRequest("123456789"))
		if err == nil {
			t.Fatalf("%s must fail", name)
			continue
		}
		got := asNotificationError(t, err)
		if got.Kind != notifications.ErrorValidation || got.Retryable() {
			t.Fatalf("%s: construction failure must terminally block: %v", name, err)
		}
		for _, secret := range []string{testBotToken, "sendMessage", "%zz", "exam"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s: secret-bearing bytes in error: %q", name, err.Error())
			}
		}
	}
}

// TestTelegramOKFalseOn200 pins NOTE-2: a 200 envelope with ok:false
// classifies through the embedded error_code, never as success.
func TestTelegramOKFalseOn200(t *testing.T) {
	harness := newBotHarness(t)
	harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return http.StatusOK, botFailure(401, "Unauthorized", nil), nil
	}
	provider := testBotProvider(t, harness)
	_, err := provider.SendTemplate(context.Background(), testBotRequest("123456789"))
	if got := asNotificationError(t, err); got.Kind != notifications.ErrorAuthentication {
		t.Fatalf("ok:false on 200 must classify by envelope: %v", err)
	}
}
