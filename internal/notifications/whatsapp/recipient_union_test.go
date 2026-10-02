package whatsapp

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// TestWhatsAppSendKeepsStrictE164 proves the frozen Phase 7A send
// subset survived the Phase 10 recipient union: Telegram-shaped
// recipients enqueue provider-neutrally but block terminally at the
// WhatsApp send boundary, before any network call. No misdirected
// send is possible.
func TestWhatsAppSendKeepsStrictE164(t *testing.T) {
	for _, recipient := range []string{"12", "@operations", "-1001234567890", "abc", ""} {
		harness := newGraphHarness(t)
		provider := testGraphProvider(t, harness)
		req := testSendRequest()
		req.Recipient = recipient
		_, err := provider.SendTemplate(context.Background(), req)
		if err == nil {
			t.Fatalf("recipient %q must block at send", recipient)
		}
		var notificationErr *notifications.NotificationError
		if !notifications.AsNotificationError(err, &notificationErr) ||
			notificationErr.Kind != notifications.ErrorValidation || notificationErr.Retryable() {
			t.Fatalf("recipient %q must terminally block: %v", recipient, err)
		}
		if len(harness.recorded()) != 0 {
			t.Fatalf("recipient %q must block before any network call", recipient)
		}
	}
	// Strict subset still sends.
	harness := newGraphHarness(t)
	provider := testGraphProvider(t, harness)
	if _, err := provider.SendTemplate(context.Background(), testSendRequest()); err != nil {
		t.Fatalf("E.164 recipient must send: %v", err)
	}
	if len(harness.recorded()) != 1 {
		t.Fatalf("E.164 recipient must send exactly once")
	}
}
