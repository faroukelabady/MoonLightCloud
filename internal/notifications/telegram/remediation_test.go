package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

func TestR1IncompleteEnvelope(t *testing.T) {
	for _, body := range []string{`{}`, `{"ok":true,"ok":false,"error_code":429,"description":"retry"}`, `{"OK":true,"result":{"message_id":42,"chat":{"id":111}}}`, `{"ok":true,"error_code":null,"result":{"message_id":42,"chat":{"id":111}}}`, `{"ok":null}`, `{"ok":false}`, `{"result":{"message_id":42,"chat":{"id":111}}}`, `{"ok":"true"}`, `{"ok":false,"error_code":0,"description":"error"}`, `{"ok":false,"error_code":200,"description":"error"}`, `{"ok":false,"error_code":403}`, `{"ok":false,"error_code":null,"description":"error"}`, `{"ok":false,"error_code":"403","description":"error"}`, `{"ok":true}`, `{"ok":true,"result":{"message_id":42}}`, `{"ok":true,"result":{"message_id":42,"chat":{"id":111}},"error_code":403}`, `{"ok":false,"error_code":403,"description":"error","result":{"message_id":42,"chat":{"id":111}}}`, `{"ok":true,"result":{"message_id":42,"chat":{"id":111}}} garbage`, `{"ok":true,"result":{"message_id":42,"chat":{"id":111}}} {}`, `{"ok":true,`} {
		t.Run(body, func(t *testing.T) {
			_, err := acceptResponse([]byte(body))
			if err == nil || asNotificationError(t, err).Kind != notifications.ErrorAmbiguous {
				t.Fatalf("must be ambiguous: %v", err)
			}
		})
	}
}

func TestR1RecipientBoundary(t *testing.T) {
	harness := newBotHarness(t)
	provider := testBotProvider(t, harness)
	for _, bad := range []string{"0", "-0", "00", "-00", strings.Repeat("0", 16), "-" + strings.Repeat("0", 16), " 1", "1 ", "1\n", "+1", "1.0", "1e3", "@" + strings.Repeat("a", 33)} {
		_, err := provider.SendTemplate(context.Background(), testBotRequest(bad))
		if err == nil || asNotificationError(t, err).Kind != notifications.ErrorValidation {
			t.Fatalf("bad recipient accepted: %q %v", bad, err)
		}
	}
	if len(harness.recorded()) != 0 {
		t.Fatal("invalid recipient reached network")
	}
	for _, good := range []string{"1", "-1", "0001", "-0001", "9999999999999999", "-9999999999999999", "@" + strings.Repeat("a", 32)} {
		got, err := ValidateRecipient(good)
		if err != nil || got != good {
			t.Fatalf("identity not preserved: %q %q %v", good, got, err)
		}
	}
}

func TestR1UsernameBound(t *testing.T) {
	maximum := "@" + strings.Repeat("a", 32)
	if got, err := ValidateRecipient(maximum); err != nil || got != maximum {
		t.Fatalf("documented maximum rejected: %v", err)
	}
	if _, err := ValidateRecipient(maximum + "a"); err == nil {
		t.Fatal("maximum+1 accepted")
	}
}
