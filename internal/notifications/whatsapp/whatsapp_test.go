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
	for _, status := range []string{"held_for_quality_assessment", "paused", "future_pacing_state"} {
		t.Run("paced status retained: "+status, func(t *testing.T) {
			harness := newGraphHarness(t)
			provider := testGraphProvider(t, harness)
			harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
				return http.StatusOK, map[string]any{
					"messages": []any{map[string]any{"id": "wamid.paced1", "message_status": status}},
				}, nil
			}
			result, err := provider.SendTemplate(context.Background(), testSendRequest())
			if err != nil {
				t.Fatalf("paced ID must accept: %v", err)
			}
			if result.ProviderMessageID != "wamid.paced1" {
				t.Fatalf("paced ID persisted: %q", result.ProviderMessageID)
			}
		})
	}
}

// TestWhatsAppProviderReflection proves machine-only diagnostics: a
// hostile 400 body reflecting the recipient, English and Arabic
// parameters, and all configured secrets yields an error carrying only
// the numeric Meta code — no private content in any form.
func TestWhatsAppProviderReflection(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
		return http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code":    "100",
			"message": "user 201012345678 param Moon Light param \u0645\u0643\u062a\u0628\u0629 token EAATestAccessToken secret test-app-secret verify test-verify-token end",
		}}, nil
	}
	request := testSendRequest()
	request.Resolved.Parameters = map[string]string{"name": "Moon Light", "message": "\u0645\u0643\u062a\u0628\u0629"}
	_, err := provider.SendTemplate(context.Background(), request)
	text := err.Error()
	if text != "validation: whatsapp provider rejected request" {
		t.Fatalf("machine-only error, got %q", text)
	}
	for _, private := range []string{"201012345678", "Moon Light", "\u0645\u0643\u062a\u0628\u0629", "EAATestAccessToken", "test-app-secret", "test-verify-token", "user", "param", "token"} {
		if strings.Contains(text, private) {
			t.Fatalf("reflected content leaked: %q in %q", private, text)
		}
	}
}

// TestWhatsAppErrorSubcodeIgnored proves numeric subcodes never
// cross the adapter boundary either: only the fixed machine text
// leaves, regardless of code/subcode content.
func TestWhatsAppErrorSubcodeIgnored(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
		return http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code": "100", "error_subcode": "987654321012345",
			"message": "user 201012345678 should never surface",
		}}, nil
	}
	_, err := provider.SendTemplate(context.Background(), testSendRequest())
	text := err.Error()
	if text != "validation: whatsapp provider rejected request" {
		t.Fatalf("got %q", text)
	}
	for _, private := range []string{"987654321012345", "201012345678", "100"} {
		if strings.Contains(text, private) {
			t.Fatalf("leaked %q in %q", private, text)
		}
	}
}

// TestWhatsAppLongSecretBoundary proves overlong reflected secrets
// cannot survive as fragments: with machine-only diagnostics there is
// no prose to truncate, so nothing private can leak at any boundary.
func TestWhatsAppLongSecretBoundary(t *testing.T) {
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	longToken := strings.Repeat("A", 300)
	harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
		return http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code":    "1",
			"message": "leaked " + longToken + " tail",
		}}, nil
	}
	_, err := provider.SendTemplate(context.Background(), testSendRequest())
	text := err.Error()
	if text != "validation: whatsapp provider rejected request" {
		t.Fatalf("got %q", text)
	}
	for _, fragment := range []string{longToken[:50], longToken[100:200], longToken[200:]} {
		if strings.Contains(text, fragment) {
			t.Fatalf("secret fragment leaked: %.20q...", fragment)
		}
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

// TestWhatsAppNumericPrivacyMatrix proves no provider-controlled
// numeric value — recipient, credential, short/long/leading-zero — can
// cross the adapter boundary in code, subcode, or prose, as quoted
// string or bare JSON number.
func TestWhatsAppNumericPrivacyMatrix(t *testing.T) {
	const safe = "validation: whatsapp provider rejected request"
	cases := []struct {
		name string
		body map[string]any
		hide []string
	}{
		{"recipient in code", map[string]any{"error": map[string]any{
			"code": "155598765432109", "message": "ignored prose"}},
			[]string{"155598765432109"}},
		{"recipient in numeric code", map[string]any{"error": map[string]any{
			"code": 155598765432109, "message": "ignored"}},
			[]string{"155598765432109"}},
		{"numeric credential in code", map[string]any{"error": map[string]any{
			"code": "9876543210987654", "message": "x"}},
			[]string{"9876543210987654"}},
		{"short parameter in code", map[string]any{"error": map[string]any{
			"code": "1234", "message": "x"}},
			[]string{"1234"}},
		{"16-digit value in code", map[string]any{"error": map[string]any{
			"code": "1234567890123456", "message": "x"}},
			[]string{"1234567890123456"}},
		{"leading-zero value in subcode", map[string]any{"error": map[string]any{
			"code": "100", "error_subcode": "001234567890"}},
			[]string{"001234567890", "100"}},
		{"oversized numeric value", map[string]any{"error": map[string]any{
			"code": "123456789012345678901234567890", "message": "x"}},
			[]string{"123456789012345678901234567890"}},
		{"mixed adversarial fields", map[string]any{"error": map[string]any{
			"code": "155598765432109", "error_subcode": "987654321012345",
			"message": "user 201012345678", "title": "Moon Light",
			"details": "test-app-secret", "error_user_msg": "EAATestAccessToken",
		}}, []string{"155598765432109", "987654321012345", "201012345678",
			"Moon Light", "test-app-secret", "EAATestAccessToken"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newGraphHarness(t)
			provider := testGraphProvider(t, harness)
			harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
				return http.StatusBadRequest, tc.body, nil
			}
			_, err := provider.SendTemplate(context.Background(), testSendRequest())
			text := err.Error()
			if text != safe {
				t.Fatalf("fixed diagnostic, got %q", text)
			}
			for _, private := range tc.hide {
				if strings.Contains(text, private) {
					t.Fatalf("leaked %q in %q", private, text)
				}
			}
			var notificationErr *notifications.NotificationError
			if !notifications.AsNotificationError(err, &notificationErr) ||
				notificationErr.Kind != notifications.ErrorValidation {
				t.Fatalf("kind Validation: %v", err)
			}
		})
	}
}

// TestWhatsAppStatusEquivalence proves the external diagnostic depends
// only on HTTP status, never on arbitrary provider body fields.
func TestWhatsAppStatusEquivalence(t *testing.T) {
	bodies := []map[string]any{
		{"error": map[string]any{"code": "100", "message": "a"}},
		{"error": map[string]any{"code": "155598765432109", "message": "b"}},
		{"nonsense": true},
		{},
	}
	for _, body := range bodies {
		harness := newGraphHarness(t)
		provider := testGraphProvider(t, harness)
		harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
			return http.StatusBadRequest, body, nil
		}
		_, err := provider.SendTemplate(context.Background(), testSendRequest())
		if text := err.Error(); text != "validation: whatsapp provider rejected request" {
			t.Fatalf("body %v -> %q", body, text)
		}
	}
}

// TestWhatsAppUnclassified4xxPermanent proves unclassified permanent
// HTTP 4xx responses classify as non-retryable Validation (never an
// infinite Temporary retry), while explicitly retryable 408/429 keep
// their retryable kinds regardless of body content.
func TestWhatsAppUnclassified4xxPermanent(t *testing.T) {
	permanent := []int{400, 402, 404, 405, 406, 410, 412, 413, 415, 421, 423, 424, 425, 426, 428, 431, 451}
	for _, status := range permanent {
		t.Run(http.StatusText(status), func(t *testing.T) {
			harness := newGraphHarness(t)
			provider := testGraphProvider(t, harness)
			harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
				return status, map[string]any{"error": map[string]any{
					"code": "99999", "message": "refused",
				}}, nil
			}
			_, err := provider.SendTemplate(context.Background(), testSendRequest())
			var notificationErr *notifications.NotificationError
			if !notifications.AsNotificationError(err, &notificationErr) ||
				notificationErr.Kind != notifications.ErrorValidation || notificationErr.Retryable() {
				t.Fatalf("status %d must be non-retryable Validation: %v", status, err)
			}
		})
	}
	retryable := []struct {
		status int
		kind   notifications.ErrorKind
	}{
		{http.StatusRequestTimeout, notifications.ErrorTemporary},
		{http.StatusTooManyRequests, notifications.ErrorRateLimited},
	}
	for _, tc := range retryable {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			harness := newGraphHarness(t)
			provider := testGraphProvider(t, harness)
			harness.script = func(graphRecordedRequest) (int, any, map[string]string) {
				// Body content must not override the explicit
				// retryable classification.
				return tc.status, map[string]any{"error": map[string]any{
					"code": "99999", "message": "slow",
				}}, map[string]string{"Retry-After": "5"}
			}
			_, err := provider.SendTemplate(context.Background(), testSendRequest())
			var notificationErr *notifications.NotificationError
			if !notifications.AsNotificationError(err, &notificationErr) ||
				notificationErr.Kind != tc.kind || !notificationErr.Retryable() {
				t.Fatalf("status %d must stay retryable %q: %v", tc.status, tc.kind, err)
			}
		})
	}
}
