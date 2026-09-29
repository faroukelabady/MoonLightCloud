package operations

import (
	"strings"
	"testing"
	"time"
)

func TestClosedVocabulary(t *testing.T) {
	for _, rule := range []string{RuleDeviceOffline, RuleDeviceSyncFailed, RuleDeviceSyncStale,
		RuleReportBlocked, RuleReportStale, RuleNotificationBlocked, RuleNotificationAmb, RuleNotificationStale} {
		if err := ValidateRule(rule); err != nil {
			t.Fatalf("rule %s: %v", rule, err)
		}
	}
	if err := ValidateRule("REBOOT_FLEET"); err == nil {
		t.Fatal("generic rules rejected")
	}
	if IsStateful(RuleDeviceSyncFailed) || IsStateful(RuleReportBlocked) || IsStateful(RuleNotificationAmb) {
		t.Fatal("terminal events are not stateful")
	}
	if !IsStateful(RuleDeviceOffline) || !IsStateful(RuleDeviceSyncStale) || !IsStateful(RuleReportStale) || !IsStateful(RuleNotificationStale) {
		t.Fatal("conditions are stateful")
	}
	if SeverityFor(RuleNotificationAmb) != SeverityUrgent || SeverityFor(RuleDeviceSyncFailed) != SeverityUrgent {
		t.Fatal("urgent assignment")
	}
	if SeverityFor(RuleDeviceOffline) != SeverityWarning {
		t.Fatal("warning default")
	}
}

func TestLabelSafety(t *testing.T) {
	if err := ValidateLabel("shop-pc-1"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", strings.Repeat("x", 65), "line\nbreak", "tab\there", "ctl\x00"} {
		if err := ValidateLabel(bad); err == nil {
			t.Fatalf("rejected: %q", bad)
		}
	}
	if got := SafeSubject("dev-id", "ok label"); got != "dev-id (ok label)" {
		t.Fatalf("subject: %q", got)
	}
	if got := SafeSubject("dev-id", "a\nb"); got != "dev-id (ab)" {
		t.Fatalf("sanitized: %q", got)
	}
}

func TestAlertBodyBounded(t *testing.T) {
	for _, locale := range []string{"ar", "en"} {
		body, err := AlertBody(locale, EventOpened, RuleDeviceOffline, "dev (shop)", time.Now(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len([]byte(body)) > MaxAlertBodyBytes {
			t.Fatal("body within parameter budget")
		}
	}
	if _, err := AlertBody("en", EventOpened, RuleDeviceOffline, strings.Repeat("x", 2000), time.Now(), ""); err == nil {
		t.Fatal("oversize body rejected, never truncated")
	}
}

func TestKeyFormulae(t *testing.T) {
	key := AlertIdempotencyKey("incident-id", EventOpened, "delivery-id")
	if !strings.HasPrefix(key, "ops-alert:") || len(key) > 128 {
		t.Fatalf("key: %q", key)
	}
	for _, pii := range []string{"201012345678", "secret", "body-text"} {
		if strings.Contains(key, pii) {
			t.Fatalf("no PII in key: %q", key)
		}
	}
	rkey := ReconnectIdempotencyKey("incident-id")
	if rkey != "ops-reconnect-sync:incident-id" {
		t.Fatalf("reconnect key: %q", rkey)
	}
	if !OpsOwnedKey("ops-alert:x:y:z", "anything") || !OpsOwnedKey("k", TemplateOpen) {
		t.Fatal("owned detection")
	}
	if OpsOwnedKey("plain-key", "daily_business_report_v1") {
		t.Fatal("business notifications are not ops-owned")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	enc := EncodeCursor(at, "some-id")
	gotAt, gotID, err := DecodeCursor(enc)
	if err != nil || !gotAt.Equal(at) || gotID != "some-id" {
		t.Fatalf("round trip: %v %v %v", gotAt, gotID, err)
	}
	if _, _, err := DecodeCursor("%%%"); err == nil {
		t.Fatal("bad cursor rejected")
	}
}
