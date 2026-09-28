package whatsapp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// TestWhatsAppSendExactBody proves the outbound contract: versioned
// path, Bearer header auth, template JSON with ordered BODY text
// parameters, and no credential anywhere in the URL.
func TestWhatsAppSendExactBody(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	result, err := provider.SendTemplate(context.Background(), testSendRequest())
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.ProviderMessageID != "wamid.accepted0001" {
		t.Fatalf("message id: %q", result.ProviderMessageID)
	}
	requests := harness.recorded()
	if len(requests) != 1 {
		t.Fatalf("requests: %d", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost {
		t.Fatalf("method: %s", request.Method)
	}
	if request.Path != "/v25.0/106540352242922/messages" {
		t.Fatalf("path: %s", request.Path)
	}
	if request.Auth != "Bearer EAATestAccessToken" {
		t.Fatalf("auth: %q", request.Auth)
	}
	body := request.Body
	if body["messaging_product"] != "whatsapp" || body["to"] != "201012345678" || body["type"] != "template" {
		t.Fatalf("envelope: %v", body)
	}
	template, ok := body["template"].(map[string]any)
	if !ok || template["name"] != "moonlight_operator_test_ar" {
		t.Fatalf("template: %v", body["template"])
	}
	language, ok := template["language"].(map[string]any)
	if !ok || language["code"] != "ar" {
		t.Fatalf("language: %v", template["language"])
	}
	components, ok := template["components"].([]any)
	if !ok || len(components) != 1 {
		t.Fatalf("components: %v", template["components"])
	}
	component, ok := components[0].(map[string]any)
	if !ok || component["type"] != "body" {
		t.Fatalf("component: %v", components[0])
	}
	parameters, ok := component["parameters"].([]any)
	if !ok || len(parameters) != 2 {
		t.Fatalf("parameters: %v", component["parameters"])
	}
	first, _ := parameters[0].(map[string]any)
	second, _ := parameters[1].(map[string]any)
	if first["type"] != "text" || first["text"] != "Moon Light" ||
		second["type"] != "text" || second["text"] != "test body" {
		t.Fatalf("parameter order/content: %v", parameters)
	}
}

// TestWhatsAppSendArabic proves exact UTF-8 Arabic parameters with no
// manual escaping damage.
func TestWhatsAppSendArabic(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	request := testSendRequest()
	request.Resolved.Parameters = map[string]string{
		"name": "مكتبة البردي", "message": "تقرير المبيعات: ١٢٬٥٠٠ جنيه\nسطر ثانٍ",
	}
	if _, err := provider.SendTemplate(context.Background(), request); err != nil {
		t.Fatalf("send: %v", err)
	}
	raw := string(harness.recorded()[0].Raw)
	for _, want := range []string{"مكتبة البردي", "تقرير المبيعات", "سطر ثانٍ"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("arabic missing %q: %s", want, raw)
		}
	}
}

// TestWhatsAppSendNoParameters omits components for zero-parameter
// templates.
func TestWhatsAppSendNoParameters(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	request := testSendRequest()
	request.Resolved.ParameterOrder = nil
	request.Resolved.Parameters = map[string]string{}
	if _, err := provider.SendTemplate(context.Background(), request); err != nil {
		t.Fatalf("send: %v", err)
	}
	template := harness.recorded()[0].Body["template"].(map[string]any)
	if _, ok := template["components"]; ok {
		t.Fatalf("zero-parameter template must omit components: %v", template)
	}
}

// TestWhatsAppStatusMatrix proves provider error taxonomy through the
// real client.
func TestWhatsAppStatusMatrix(t *testing.T) {
	graphErr := func(code, message string) map[string]any {
		return map[string]any{"error": map[string]any{"code": code, "message": message, "type": "OAuthException"}}
	}
	cases := []struct {
		name      string
		status    int
		body      map[string]any
		wantKind  notifications.ErrorKind
		retryable bool
	}{
		{"400 validation", http.StatusBadRequest, graphErr("100", "Bad param."), notifications.ErrorValidation, false},
		{"401 authentication", http.StatusUnauthorized, graphErr("190", "Denied."), notifications.ErrorAuthentication, false},
		{"403 authentication", http.StatusForbidden, graphErr("200", "Forbidden."), notifications.ErrorAuthentication, false},
		{"408 temporary", http.StatusRequestTimeout, graphErr("1", "Slow."), notifications.ErrorTemporary, true},
		{"409 conflict", http.StatusConflict, graphErr("1", "Clash."), notifications.ErrorConflict, false},
		{"422 validation", http.StatusUnprocessableEntity, graphErr("100", "Bad."), notifications.ErrorValidation, false},
		{"429 rate limited", http.StatusTooManyRequests, graphErr("80004", "Slow down."), notifications.ErrorRateLimited, true},
		{"500 temporary", http.StatusInternalServerError, graphErr("1", "Down."), notifications.ErrorTemporary, true},
		{"503 temporary", http.StatusServiceUnavailable, graphErr("1", "Down."), notifications.ErrorTemporary, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newGraphHarness(t)
			provider := testGraphProvider(t, harness)
			harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
				return tc.status, tc.body, nil
			}
			_, err := provider.SendTemplate(context.Background(), testSendRequest())
			var notificationErr *notifications.NotificationError
			if !notifications.AsNotificationError(err, &notificationErr) ||
				notificationErr.Kind != tc.wantKind || notificationErr.Retryable() != tc.retryable {
				t.Fatalf("got %v want kind %q retryable %v", err, tc.wantKind, tc.retryable)
			}
		})
	}
}

// TestWhatsAppAmbiguousResponses proves 2xx without exactly one valid
// ID is Ambiguous, never accepted: Meta may already have sent.
func TestWhatsAppAmbiguousResponses(t *testing.T) {
	bodies := map[string]map[string]any{
		"missing messages": {},
		"empty messages":   {"messages": []any{}},
		"empty id":         {"messages": []any{map[string]any{"id": ""}}},
		"two ids": {"messages": []any{
			map[string]any{"id": "wamid.1"}, map[string]any{"id": "wamid.2"},
		}},
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			harness := newGraphHarness(t)
			provider := testGraphProvider(t, harness)
			harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
				return http.StatusOK, body, nil
			}
			_, err := provider.SendTemplate(context.Background(), testSendRequest())
			var notificationErr *notifications.NotificationError
			if !notifications.AsNotificationError(err, &notificationErr) ||
				notificationErr.Kind != notifications.ErrorAmbiguous || notificationErr.Retryable() {
				t.Fatalf("must be non-retryable Ambiguous: %v", err)
			}
		})
	}
	t.Run("malformed json", func(t *testing.T) {
		harness := newGraphHarness(t)
		provider := testGraphProvider(t, harness)
		harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
			return http.StatusOK, "not-json{{{", nil
		}
		_, err := provider.SendTemplate(context.Background(), testSendRequest())
		var notificationErr *notifications.NotificationError
		if !notifications.AsNotificationError(err, &notificationErr) ||
			notificationErr.Kind != notifications.ErrorAmbiguous {
			t.Fatalf("malformed 2xx must be Ambiguous: %v", err)
		}
	})
	t.Run("held for quality is not accepted", func(t *testing.T) {
		harness := newGraphHarness(t)
		provider := testGraphProvider(t, harness)
		harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
			return http.StatusOK, map[string]any{
				"messages": []any{map[string]any{"id": "wamid.held1", "message_status": "held_for_quality_assessment"}},
			}, nil
		}
		_, err := provider.SendTemplate(context.Background(), testSendRequest())
		var notificationErr *notifications.NotificationError
		if !notifications.AsNotificationError(err, &notificationErr) ||
			notificationErr.Kind != notifications.ErrorValidation {
			t.Fatalf("held message must block, not accept: %v", err)
		}
	})
}

// TestWhatsAppCredentialReflection proves reflected secrets are
// scrubbed before diagnostic bounding: no prefix leak.
func TestWhatsAppCredentialReflection(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
		return http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code":    "1",
			"message": "token EAATestAccessToken secret test-app-secret verify test-verify-token leaked",
		}}, nil
	}
	_, err := provider.SendTemplate(context.Background(), testSendRequest())
	text := err.Error()
	for _, secret := range []string{"EAATestAccessToken", "test-app-secret", "test-verify-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("reflected secret leaked: %q", text)
		}
	}
	if !strings.Contains(text, "[redacted]") {
		t.Fatalf("redaction marker missing: %q", text)
	}
}

// TestWhatsAppRetryAfter proves 429 honors bounded Retry-After.
func TestWhatsAppRetryAfter(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
		return http.StatusTooManyRequests,
			map[string]any{"error": map[string]any{"code": "80004", "message": "Slow."}},
			map[string]string{"Retry-After": "45"}
	}
	_, err := provider.SendTemplate(context.Background(), testSendRequest())
	var notificationErr *notifications.NotificationError
	if !notifications.AsNotificationError(err, &notificationErr) ||
		notificationErr.Kind != notifications.ErrorRateLimited {
		t.Fatalf("429: %v", err)
	}
	if after, ok := notificationErr.GetRetryAfter(); !ok || after.Seconds() != 45 {
		t.Fatalf("retry-after: %v %v", after, ok)
	}
}

// TestWhatsAppAfterWriteDisconnect proves an accepted-then-dropped
// connection is Ambiguous with no automatic retry: the request reached
// the server.
func TestWhatsAppAfterWriteDisconnect(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	harness.hangup = true
	_, err := provider.SendTemplate(context.Background(), testSendRequest())
	var notificationErr *notifications.NotificationError
	if !notifications.AsNotificationError(err, &notificationErr) ||
		notificationErr.Kind != notifications.ErrorAmbiguous || notificationErr.Retryable() {
		t.Fatalf("after-write disconnect must be Ambiguous: %v", err)
	}
	if len(harness.recorded()) != 1 {
		t.Fatalf("one attempt: %d", len(harness.recorded()))
	}
}

// TestWhatsAppBeforeWriteFailure proves a never-written request is a
// safe Temporary retry.
func TestWhatsAppBeforeWriteFailure(t *testing.T) {
	if err := classifyTransport(context.Background(), errTestTransport(), false); err == nil {
		t.Fatal("must fail")
	} else {
		var notificationErr *notifications.NotificationError
		if !notifications.AsNotificationError(err, &notificationErr) ||
			notificationErr.Kind != notifications.ErrorTemporary || !notificationErr.Retryable() {
			t.Fatalf("before-write must be Temporary: %v", err)
		}
	}
	if err := classifyTransport(context.Background(), errTestTransport(), true); err == nil {
		t.Fatal("must fail")
	} else {
		var notificationErr *notifications.NotificationError
		if !notifications.AsNotificationError(err, &notificationErr) ||
			notificationErr.Kind != notifications.ErrorAmbiguous {
			t.Fatalf("after-write must be Ambiguous: %v", err)
		}
	}
}

func errTestTransport() error { return errTestTransportValue }

var errTestTransportValue = errTransportTest("connection refused")

type errTransportTest string

func (e errTransportTest) Error() string { return string(e) }

// TestWhatsAppNoSecretInURL proves no credential travels in URL/query.
func TestWhatsAppNoSecretInURL(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	if _, err := provider.SendTemplate(context.Background(), testSendRequest()); err != nil {
		t.Fatalf("send: %v", err)
	}
	request := harness.recorded()[0]
	if request.RawQuery != "" {
		t.Fatalf("query must be empty: %q", request.RawQuery)
	}
	if request.Path != "/v25.0/106540352242922/messages" {
		t.Fatalf("path: %s", request.Path)
	}
}

// TestWhatsAppLongSecretBoundary proves scrub-before-bound: a
// reflected overlong secret is redacted in full (no surviving prefix
// fragment) and diagnostics stay bounded.
func TestWhatsAppLongSecretBoundary(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	longToken := strings.Repeat("A", 300)
	provider.scrub = newSecretScrubber(longToken)
	harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
		return http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code":    "1",
			"message": "leaked " + longToken + " tail",
		}}, nil
	}
	_, err := provider.SendTemplate(context.Background(), testSendRequest())
	text := err.Error()
	if strings.Contains(text, longToken) {
		t.Fatal("full secret leaked")
	}
	for _, fragment := range []string{longToken[:50], longToken[100:200], longToken[200:]} {
		if strings.Contains(text, fragment) {
			t.Fatalf("secret fragment leaked: %.20q...", fragment)
		}
	}
	if len(text) > 300 {
		t.Fatalf("diagnostic unbounded: %d", len(text))
	}
}
