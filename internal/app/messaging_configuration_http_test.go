package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/google/uuid"
)

func TestMessagingConfigurationHTTP(t *testing.T) {
	url := testutil.Isolated(t)
	cfg := testConfig(t, url)
	cfg.TelegramNotifications = config.TelegramNotificationConfig{Enabled: true, ProviderKey: "telegram-main", BotToken: "123456:AAA-fake-test-only", HTTPTimeout: time.Second}
	cfg.WhatsAppNotifications = config.WhatsAppNotificationConfig{Enabled: true, ProviderKey: "whatsapp-main", PhoneNumberID: "123456789", AccessToken: "fake-test-token", AppSecret: "fake-test-secret", WebhookVerifyToken: "fake-test-verify", GraphVersion: "v25.0", HTTPTimeout: time.Second}
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	clock := humanKit(t, a)
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	e := &authEnv{t: t, a: a, srv: srv, clk: clock, url: url}
	store := uuid.NewString()
	e.store(store)
	root := fullOwner(t, a, srv, clock, "report-owner@shop.test")
	limited := root.createUser(srv, "report-limited@shop.test", "owner", false, store)
	base := "/api/v1/dashboard/settings/messaging"
	expect := func(h *human, method, path string, body any, want int) map[string]any {
		t.Helper()
		code, result := h.do(method, path, body)
		if code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, code, want, result["_raw"])
		}
		return result
	}
	for _, route := range []struct{ method, path string }{{"GET", base + "/recipients/" + uuid.NewString()}, {"PUT", base + "/recipients/" + uuid.NewString()}, {"DELETE", base + "/recipients/" + uuid.NewString()}, {"PUT", base + "/schedules/" + uuid.NewString()}, {"DELETE", base + "/schedules/" + uuid.NewString()}, {"GET", base + "/runs"}} {
		expect(limited, route.method, route.path, map[string]any{}, 403)
	}
	expect(root, "POST", base+"/recipients", map[string]any{"label": "Bad number", "provider_key": "whatsapp-main", "recipient": "0201012345678", "locale": "en"}, 400)
	recipient := expect(root, "POST", base+"/recipients", map[string]any{"label": "Owner", "provider_key": "telegram-main", "recipient": "123456789", "locale": "ar"}, 201)
	id := recipient["id"].(string)
	expect(root, "GET", base+"/recipients/"+id, nil, 200)
	response, err := root.client.Get(srv.URL + base + "/recipients/" + id)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("full address editor response can be cached")
	}
	response.Body.Close()
	listed := expect(root, "GET", base, nil, 200)
	row := listed["recipients"].([]any)[0].(map[string]any)
	if _, exists := row["recipient"]; exists || row["recipient_masked"] != "…6789" {
		t.Fatal("full address leaked in list")
	}
	changes := map[string]any{"label": "Edited owner", "recipient": "987654321", "locale": "en"}
	if code, _ := root.raw("PUT", base+"/recipients/"+id, changes, map[string]string{"Origin": root.base}); code != 403 {
		t.Fatal("edit without CSRF accepted")
	}
	expect(root, "PUT", base+"/recipients/"+id, changes, 200)
	if value := expect(root, "GET", base+"/recipients/"+id, nil, 200); value["recipient"] != "987654321" || value["label"] != "Edited owner" {
		t.Fatal("recipient edit not persisted")
	}
	schedule := expect(root, "POST", base+"/schedules", map[string]any{"name": "Daily", "kind": "daily", "local_time": "21:00", "recipient_ids": []string{id}}, 201)
	scheduleID := schedule["id"].(string)
	edit := map[string]any{"name": "Ten-day", "kind": "ten_day", "local_time": "20:00", "anchor_local_date": "2026-11-08", "recipient_ids": []string{id}, "revision": schedule["revision"]}
	updated := expect(root, "PUT", base+"/schedules/"+scheduleID, edit, 200)
	if updated["kind"] != "ten_day" || updated["id"] != scheduleID || updated["revision"].(float64) != 2 {
		t.Fatal("schedule update mismatch")
	}
	stale := expect(root, "PUT", base+"/schedules/"+scheduleID, edit, 409)
	if !strings.Contains(stale["_raw"].(string), "SCHEDULE_CHANGED") {
		t.Fatal("stale edit has no useful reason")
	}
	for _, query := range []string{"?limit=0", "?limit=51", "?limit=abc", "?before=not-a-uuid"} {
		expect(root, "GET", base+"/runs"+query, nil, 400)
	}
	page := expect(root, "GET", base+"/runs?limit=10", nil, 200)
	if page["next_cursor"] != "" || len(page["runs"].([]any)) != 0 {
		t.Fatal("empty history page malformed")
	}
	expect(root, "POST", base+"/schedules/"+scheduleID+"/run", map[string]any{"request_key": "delivery-status-read"}, 200)
	page = expect(root, "GET", base+"/runs?limit=10", nil, 200)
	states := page["runs"].([]any)[0].(map[string]any)["deliveries"].([]any)
	if len(states) != 1 {
		t.Fatal("missing per-recipient outcome")
	}
	state := states[0].(map[string]any)
	if state["provider_kind"] != "telegram" || state["recipient_masked"] != "…4321" || state["report_status"] != "pending" || state["dispatch_status"] != "" {
		t.Fatalf("incorrect delivery status metadata: %v", state)
	}
	for _, key := range []string{"recipient", "report_body", "provider_message_id", "parameters"} {
		if _, ok := state[key]; ok {
			t.Fatalf("private field exposed: %s", key)
		}
	}

	expect(root, "DELETE", base+"/schedules/"+scheduleID, nil, 200)
	expect(root, "POST", base+"/schedules/"+scheduleID+"/run", map[string]any{"request_key": "deleted-run"}, 404)
	expect(root, "DELETE", base+"/recipients/"+id, nil, 200)
	expect(root, "GET", base+"/recipients/"+id, nil, 404)
	listed = expect(root, "GET", base, nil, 200)
	if len(listed["recipients"].([]any)) != 0 || len(listed["schedules"].([]any)) != 0 {
		t.Fatal("deleted configurations remain active")
	}
}
