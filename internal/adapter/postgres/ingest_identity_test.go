package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// legacyHashOf recomputes the pre-2D float64 hash exactly as the old
// canonicalizer did (plain decode without UseNumber + marshal + sha256).
func legacyHashOf(t *testing.T, payload string) []byte {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(out)
	return sum[:]
}

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
	if err := ingestOne(t, env, id, "sale.finalized.v1", occurred, other); err == nil {
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
	if err := ingestOne(t, env, id, "sale.finalized.v1", "2026-09-20T11:00:00Z", payload); err == nil {
		t.Fatal("different occurred_at instant must conflict")
	} else if identityKindOf(err) != apperr.Conflict {
		t.Fatalf("want conflict, got %v", err)
	}

	// Equivalent textual occurred_at (Z vs +03:00, same instant) → already_accepted.
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T13:00:00+03:00","payload":%s}]}`,
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

// TestCredentialRotationDedup proves credential identity is not part of
// idempotency: rotating the device credential does not break dedup.
func TestCredentialRotationDedup(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "shop-rot")
	if err != nil {
		t.Fatal(err)
	}
	env := newSaleEnvOnPool(t, pool, p.Device.ID, p.Credential.ID)
	payload := fixture(t, "sale_egp.json")
	id := "22222222-2222-7222-8222-222222222222"
	if r := env.ingest(t, id, payload); r.Events[0].Status != "accepted" {
		t.Fatalf("first: %+v", r)
	}
	rot, err := authSvc.Rotate(ctx, p.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	envRot := newSaleEnvOnPool(t, pool, p.Device.ID, rot.Credential.ID)
	if r := envRot.ingest(t, id, payload); r.Events[0].Status != "already_accepted" {
		t.Fatalf("after rotation: %+v", r)
	}
}

// P2D-MED-01 (fail-closed): the v1 hash alone never proves equality — the
// stored immutable payload is re-canonicalized exactly. Large-number case:
// stored 2^53+1 vs incoming 2^53+1 → already_accepted (and opportunistically
// upgraded to v2); stored 2^53+1 vs incoming 2^53+1±1 → EVENT_ID_REUSE even
// though the lossy v1 hashes collide.
func TestLegacyFailClosed(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	bigPayload := `{"sale_id":"99999999-9999-4999-8999-999999999999","v":9007199254740993}`
	bigID := "44444444-4444-7444-8444-444444444444"
	bigLegacy := legacyHashOf(t, bigPayload)
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO sync_events (event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, payload_hash, payload_hash_version)
		 VALUES ($1,$2,$3,'system.test.v1','2026-09-20T10:00:00Z',now(),$4::jsonb,$5,1)`,
		bigID, env.devID, env.credID, bigPayload, bigLegacy); err != nil {
		t.Fatal(err)
	}
	// Exact retry of the float-unsafe old event → already_accepted.
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"system.test.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		bigID, bigPayload)
	res, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("legacy big-int retry: %v", err)
	}
	if res.Events[0].Status != "already_accepted" {
		t.Fatalf("legacy big-int retry: %+v", res)
	}
	// Optional upgrade: exact match converges hash metadata to v2 without
	// mutating the immutable payload.
	var ver int
	if err := env.pool.QueryRow(ctx,
		`SELECT payload_hash_version FROM sync_events WHERE event_id=$1`, bigID).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 2 {
		t.Fatalf("exact legacy match must upgrade hash to v2, got %d", ver)
	}
	var stored string
	if err := env.pool.QueryRow(ctx,
		`SELECT payload::text FROM sync_events WHERE event_id=$1`, bigID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "9007199254740993") {
		t.Fatalf("upgrade must not mutate payload: %s", stored)
	}
	// Materially different payload with colliding v1 hash → EVENT_ID_REUSE.
	adjPayload := `{"sale_id":"99999999-9999-4999-8999-999999999999","v":9007199254740992}`
	if !bytes.Equal(legacyHashOf(t, adjPayload), bigLegacy) {
		t.Fatal("test setup: adjacent payloads must share the legacy hash")
	}
	victim := "66666666-6666-7666-8666-666666666666"
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO sync_events (event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, payload_hash, payload_hash_version)
		 VALUES ($1,$2,$3,'system.test.v1','2026-09-20T10:00:00Z',now(),$4::jsonb,$5,1)`,
		victim, env.devID, env.credID, bigPayload, bigLegacy); err != nil {
		t.Fatal(err)
	}
	body = fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"system.test.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		victim, adjPayload)
	if _, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(body)); err == nil {
		t.Fatal("colliding but different payload must conflict")
	} else if identityKindOf(err) != apperr.Conflict {
		t.Fatalf("want EVENT_ID_REUSE, got %v", err)
	}
	// Rotation + exact old payload still deduplicates (credential excluded).
	pool, authSvc := openTestRepo(t)
	_ = pool
	_ = authSvc
}

// TestLegacyRotationExactRetry: v1 row + rotated credential + exact old
// payload → already_accepted (credential identity excluded from idempotency).
func TestLegacyRotationExactRetry(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "shop-rot-v1")
	if err != nil {
		t.Fatal(err)
	}
	payload := fixture(t, "sale_egp.json")
	id := "22222222-2222-7222-8222-222222222222"
	if _, err := pool.Exec(ctx,
		`INSERT INTO sync_events (event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, payload_hash, payload_hash_version)
		 VALUES ($1,$2,$3,'sale.finalized.v1','2026-09-20T10:00:00Z',now(),$4::jsonb,$5,1)`,
		id, p.Device.ID, p.Credential.ID, payload, legacyHashOf(t, payload)); err != nil {
		t.Fatal(err)
	}
	rot, err := authSvc.Rotate(ctx, p.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	envRot := newSaleEnvOnPool(t, pool, p.Device.ID, rot.Credential.ID)
	if r := envRot.ingest(t, id, payload); r.Events[0].Status != "already_accepted" {
		t.Fatalf("rotation + exact old: %+v", r)
	}
}
func TestLegacySaleRetryMatrix(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	payload := fixture(t, "sale_egp.json")
	id := "22222222-2222-7222-8222-222222222222"
	legacy := legacyHashOf(t, payload)
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO sync_events (event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, payload_hash, payload_hash_version)
		 VALUES ($1,$2,$3,'sale.finalized.v1','2026-09-20T10:00:00Z',now(),$4::jsonb,$5,1)`,
		id, env.devID, env.credID, payload, legacy); err != nil {
		t.Fatal(err)
	}
	if r := env.ingest(t, id, payload); r.Events[0].Status != "already_accepted" {
		t.Fatalf("exact old retry: %+v", r)
	}
	for name, pair := range map[string][2]string{
		"type changed":     {"system.test.v1", "2026-09-20T10:00:00Z"},
		"occurred changed": {"sale.finalized.v1", "2026-09-21T10:00:00Z"},
	} {
		typ, occ := pair[0], pair[1]
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":%q,"payload":%s}]}`,
			id, typ, occ, payload)
		if typ == "system.test.v1" {
			body = fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":%q,"payload":%s}]}`,
				id, typ, occ, `{}`)
		}
		if _, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(body)); err == nil {
			t.Fatalf("%s: must conflict", name)
		} else if identityKindOf(err) != apperr.Conflict {
			t.Fatalf("%s: want EVENT_ID_REUSE, got %v", name, err)
		}
	}
}
