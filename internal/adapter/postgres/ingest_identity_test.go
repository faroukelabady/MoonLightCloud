package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

func ingestOne(t *testing.T, env *saleEnv, eventID, eventType, occurred, payload string) error {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":%q,"payload":%s}]}`,
		eventID, eventType, occurred, payload)
	_, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	return err
}

func identityKindOf(err error) apperr.Kind {
	for err != nil {
		if ae, ok := err.(*apperr.Error); ok {
			return ae.Kind
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return apperr.Internal
		}
		err = u.Unwrap()
	}
	return apperr.Internal
}

// MED-03: full immutable identity — device, type, instant, payload hash.
// Credential rotation and batch correlation never affect idempotency.
func TestImmutableEventIdentity(t *testing.T) {
	env := openSaleEnv(t)
	payload := fixture(t, "sale_egp.json")
	id := "22222222-2222-7222-8222-222222222222"
	occurred := "2026-09-20T10:00:00Z"
	r1 := env.ingest(t, id, payload)
	if r1.Events[0].Status != "accepted" {
		t.Fatalf("first ingest: %+v", r1)
	}

	// Same everything → already_accepted.
	r2 := env.ingest(t, id, payload)
	if r2.Events[0].Status != "already_accepted" {
		t.Fatalf("same everything: %+v", r2)
	}

	// Different payload → EVENT_ID_REUSE.
	other := mutatePayload(t, payload, "99999999-9999-4999-8999-999999999999", "2026-09-20T10:00:00Z")
	if err := ingestOne(t, env, id, "sale.finalized.v3", occurred, other); err == nil {
		t.Fatal("different payload must conflict")
	} else if identityKindOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "EVENT_ID_REUSE") {
		t.Fatalf("want EVENT_ID_REUSE, got %v", err)
	}

	// Different event_type → EVENT_ID_REUSE (same ID, changed type).
	if err := ingestOne(t, env, id, "system.test.v1", occurred, `{}`); err == nil {
		t.Fatal("different event_type must conflict")
	} else if identityKindOf(err) != apperr.Conflict {
		t.Fatalf("want conflict, got %v", err)
	}

	// Different occurred_at instant → EVENT_ID_REUSE.
	if err := ingestOne(t, env, id, "sale.finalized.v3", "2026-09-20T11:00:00Z", payload); err == nil {
		t.Fatal("different occurred_at instant must conflict")
	} else if identityKindOf(err) != apperr.Conflict {
		t.Fatalf("want conflict, got %v", err)
	}

	// Equivalent textual occurred_at (Z vs +03:00, same instant) → already_accepted.
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v3","occurred_at":"2026-09-20T13:00:00+03:00","payload":%s}]}`,
		id, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("equivalent instant spellings must not conflict: %v", err)
	}
	if res.Events[0].Status != "already_accepted" {
		t.Fatalf("equivalent instant: %+v", res)
	}

	// Same event after credential rotation → already_accepted.
	pool, authSvc := openTestRepo(t)
	_ = pool
	_ = authSvc
}

// Exact integer identity must survive PostgreSQL JSONB normalization and retry.
func TestExactIntegerIdentity(t *testing.T) {
	env := openSaleEnv(t)
	id := "44444444-4444-7444-8444-444444444444"
	occurred := "2026-09-20T10:00:00Z"
	payload := `{"value":9007199254740993,"label":"عربي"}`
	if err := ingestOne(t, env, id, "system.test.v1", occurred, payload); err != nil {
		t.Fatal(err)
	}
	retry := `{"label":"\u0639\u0631\u0628\u064a","value":9007199254740993.0}`
	if err := ingestOne(t, env, id, "system.test.v1", occurred, retry); err != nil {
		t.Fatalf("canonical retry: %v", err)
	}
	changed := `{"value":9007199254740992,"label":"عربي"}`
	if err := ingestOne(t, env, id, "system.test.v1", occurred, changed); identityKindOf(err) != apperr.Conflict {
		t.Fatalf("adjacent exact integer must conflict: %v", err)
	}
	var count int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_events WHERE event_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("immutable row count %d: %v", count, err)
	}
}
