package sync

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

var (
	testDevice = "11111111-1111-7111-8111-111111111111"
	testNow    = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
)

func eventJSON(id, typ, occurred, payload string) string {
	return fmt.Sprintf(`{"event_id":%q,"event_type":%q,"occurred_at":%q,"payload":%s}`,
		id, typ, occurred, payload)
}

func batchJSON(events ...string) string {
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

func TestParseValidBatch(t *testing.T) {
	body := batchJSON(eventJSON(
		"22222222-2222-7222-8222-222222222222",
		"system.test.v1", "2026-09-19T10:20:30.123456Z", `{"b":2,"a":1}`))
	b, err := ParseBatch([]byte(body), testDevice, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Events) != 1 || b.Events[0].DeviceID != testDevice {
		t.Fatalf("bad batch: %+v", b)
	}
	// Canonical form: sorted keys regardless of input order.
	if string(b.Events[0].Payload) != `{"a":1,"b":2}` {
		t.Fatalf("payload not canonical: %s", b.Events[0].Payload)
	}
	// Same semantics, different order/whitespace → same hash.
	alt := batchJSON(eventJSON(
		"22222222-2222-7222-8222-222222222222",
		"system.test.v1", "2026-09-19T10:20:30.123456Z", `{ "a" : 1 , "b" : 2 }`))
	b2, err := ParseBatch([]byte(alt), testDevice, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if string(b2.Events[0].Hash) != string(b.Events[0].Hash) {
		t.Fatal("semantically identical payloads must hash identically")
	}
}

func TestParseFailures(t *testing.T) {
	id := "22222222-2222-7222-8222-222222222222"
	good := eventJSON(id, "system.test.v1", "2026-09-19T10:20:30Z", `{"a":1}`)
	cases := []struct {
		name string
		body string
		kind apperr.Kind
	}{
		{"empty events", `{"events":[]}`, apperr.InvalidInput},
		{"missing events", `{}`, apperr.InvalidInput},
		{"bad json", `{`, apperr.InvalidInput},
		{"trailing data", batchJSON(good) + "x", apperr.InvalidInput},
		{"dup keys top", `{"events":[], "events":[]}`, apperr.InvalidInput},
		{"dup keys event", batchJSON(`{"event_id":"` + id + `","event_id":"` + id + `","event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{}}`), apperr.InvalidInput},
		{"bad event id", batchJSON(eventJSON("nope", "system.test.v1", "2026-09-19T10:20:30Z", `{}`)), apperr.InvalidInput},
		{"missing event id", batchJSON(`{"event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{}}`), apperr.InvalidInput},
		{"bad type format", batchJSON(eventJSON(id, "NOPE", "2026-09-19T10:20:30Z", `{}`)), apperr.InvalidInput},
		{"unsupported type", batchJSON(eventJSON(id, "sale.finalized.v1", "2026-09-19T10:20:30Z", `{}`)), apperr.Unprocessable},
		{"device mismatch", batchJSON(`{"event_id":"` + id + `","event_type":"system.test.v1","device_id":"99999999-9999-7999-8999-999999999999","occurred_at":"2026-09-19T10:20:30Z","payload":{}}`), apperr.Forbidden},
		{"bad occurred", batchJSON(eventJSON(id, "system.test.v1", "yesterday", `{}`)), apperr.InvalidInput},
		{"absurd past", batchJSON(eventJSON(id, "system.test.v1", "1999-01-01T00:00:00Z", `{}`)), apperr.Unprocessable},
		{"absurd future", batchJSON(eventJSON(id, "system.test.v1", "2036-09-19T00:00:00Z", `{}`)), apperr.Unprocessable},
		{"missing payload", batchJSON(`{"event_id":"` + id + `","event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z"}`), apperr.InvalidInput},
		{"array payload", batchJSON(eventJSON(id, "system.test.v1", "2026-09-19T10:20:30Z", `[1,2]`)), apperr.InvalidInput},
		{"dup ids in batch", batchJSON(good, good), apperr.InvalidInput},
		{"bad batch id", `{"batch_id":"x","events":[` + good + `]}`, apperr.InvalidInput},
		{"oversize payload", batchJSON(eventJSON(id, "system.test.v1", "2026-09-19T10:20:30Z",
			`{"big":"`+strings.Repeat("x", MaxPayloadBytes)+`"}`)), apperr.TooLarge},
	}
	for _, tc := range cases {
		_, err := ParseBatch([]byte(tc.body), testDevice, testNow)
		if err == nil {
			t.Fatalf("%s: want error", tc.name)
			continue
		}
		ae, ok := asAppErr(err)
		if !ok || ae.Kind != tc.kind {
			t.Fatalf("%s: want kind %d, got %v", tc.name, tc.kind, err)
		}
	}
	// Too many events.
	many := make([]string, 0, MaxBatchEvents+1)
	for i := 0; i <= MaxBatchEvents; i++ {
		many = append(many, eventJSON(fmt.Sprintf("22222222-2222-7222-8222-%012d", i),
			"system.test.v1", "2026-09-19T10:20:30Z", `{}`))
	}
	if _, err := ParseBatch([]byte(batchJSON(many...)), testDevice, testNow); err == nil {
		t.Fatal("oversize batch must fail")
	} else if ae, ok := asAppErr(err); !ok || ae.Kind != apperr.TooLarge {
		t.Fatalf("want TooLarge, got %v", err)
	}
}

func asAppErr(err error) (*apperr.Error, bool) {
	// Unwrap single fmt %w layers from per-event context.
	for err != nil {
		if ae, ok := err.(*apperr.Error); ok {
			return ae, true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		err = u.Unwrap()
	}
	return nil, false
}

func TestDeviceIDEchoAcceptedWhenMatching(t *testing.T) {
	id := "22222222-2222-7222-8222-222222222222"
	body := batchJSON(`{"event_id":"` + id + `","event_type":"system.test.v1","device_id":"` +
		testDevice + `","occurred_at":"2026-09-19T10:20:30Z","payload":{}}`)
	b, err := ParseBatch([]byte(body), testDevice, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if b.Events[0].DeviceID != testDevice {
		t.Fatal("device identity must come from authentication")
	}
}

func TestCapabilitiesShape(t *testing.T) {
	svc := NewService(nil, clock.Fixed{T: testNow})
	caps := svc.Capabilities()
	if caps.APIVersion != "v1" || caps.MaxBatchEvents != MaxBatchEvents ||
		caps.MaxPayloadBytes != MaxPayloadBytes || len(caps.SupportedEvents) == 0 ||
		caps.ServerTime == "" {
		t.Fatalf("bad capabilities: %+v", caps)
	}
	var _ = json.Marshal
}
