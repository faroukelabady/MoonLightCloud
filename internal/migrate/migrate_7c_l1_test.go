package migrate_test

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// Fresh database reaches schema 18 with the named constraint enforced.
func TestFreshTo18(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var enforced bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'device_control_commands_pending_lease_null')`).Scan(&enforced); err != nil || !enforced {
		t.Fatalf("named constraint enforced: %v", err)
	}
}

// v17 → v18 succeeds with valid representative state and preserves rows.
func TestV17To18ValidUpgrade(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 17); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', now(), now(), now(), now())`); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"11111111-1111-4111-8111-111111111112", "11111111-1111-4111-8111-111111111113", "11111111-1111-4111-8111-111111111114", "11111111-1111-4111-8111-111111111115", "11111111-1111-4111-8111-111111111116"} {
		if _, err := conn.Exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
			VALUES ('` + d + `', 'shop', 'active', now(), now())`); err != nil {
			t.Fatal(err)
		}
	}
	mkCmd := func(id, dev, key, status, extraCols, extraVals string) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO device_control_commands
			(id, device_id, command_type, command_version, idempotency_key, status, requested_at, created_at, updated_at` + extraCols + `)
			VALUES ('` + id + `', '` + dev + `', 'sync_now', 1, '` + key + `', '` + status + `', now(), now(), now()` + extraVals + `)`); err != nil {
			t.Fatalf("seed %s: %v", status, err)
		}
	}
	mkCmd("22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111", "k-22222222", "pending", "", "")
	mkCmd("33333333-3333-4333-8333-333333333333", "11111111-1111-4111-8111-111111111112", "k-33333333", "leased",
		", leased_at, lease_until, lease_generation", ", now(), now() + interval '1 minute', 1")
	mkCmd("44444444-4444-4433-8444-444444444444", "11111111-1111-4111-8111-111111111113", "k-44444444", "accepted",
		", accepted_at", ", now()")
	mkCmd("55555555-5555-4533-8555-555555555555", "11111111-1111-4111-8111-111111111114", "k-55555555", "running",
		", accepted_at, running_at", ", now(), now()")
	mkCmd("66666666-6666-4633-8666-666666666666", "11111111-1111-4111-8111-111111111115", "k-66666666", "completed",
		", finished_at, result_code", ", now(), 'SYNC_COMPLETED'")
	mkCmd("77777777-7777-4733-8777-777777777777", "11111111-1111-4111-8111-111111111116", "k-77777777", "failed",
		", finished_at, result_code", ", now(), 'SYNC_FAILED_NETWORK'")
	before := snapshotCommands(t, conn)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	after := snapshotCommands(t, conn)
	if before != after {
		t.Fatalf("rows preserved:\nbefore %s\nafter %s", before, after)
	}
	// Live enforcement at 18: pending with lease rejected, NULL accepted.
	if _, err := conn.Exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('99999999-0000-4000-8000-999999999999', 'probe', 'active', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO device_control_commands
		(id, device_id, command_type, command_version, idempotency_key, status, requested_at, lease_until, created_at, updated_at)
		VALUES (gen_random_uuid(), '99999999-0000-4000-8000-999999999999', 'sync_now', 1, 'bad-lease', 'pending', now(), now(), now(), now())`); err == nil {
		t.Fatal("pending with lease_until must be rejected at 18")
	}
	if _, err := conn.Exec(`INSERT INTO device_control_commands
		(id, device_id, command_type, command_version, idempotency_key, status, requested_at, created_at, updated_at)
		VALUES (gen_random_uuid(), '99999999-0000-4000-8000-999999999999', 'sync_now', 1, 'good-null', 'pending', now(), now(), now())`); err != nil {
		t.Fatalf("pending with NULL lease accepted: %v", err)
	}
	// Invariants still hold: duplicate active and duplicate key rejected.
	if _, err := conn.Exec(`INSERT INTO device_control_commands
		(id, device_id, command_type, command_version, idempotency_key, status, requested_at, created_at, updated_at)
		VALUES (gen_random_uuid(), '11111111-1111-4111-8111-111111111111', 'sync_now', 1, 'second-active', 'pending', now(), now(), now())`); err == nil {
		t.Fatal("one-active uniqueness must hold at 18")
	}
	if _, err := conn.Exec(`INSERT INTO device_control_commands
		(id, device_id, command_type, command_version, idempotency_key, status, requested_at, finished_at, result_code, created_at, updated_at)
		VALUES (gen_random_uuid(), '11111111-1111-4111-8111-111111111111', 'sync_now', 1, 'k-22222222', 'failed', now(), now(), 'SYNC_FAILED_AUTH', now(), now())`); err == nil {
		t.Fatal("idempotency uniqueness must hold at 18")
	}
}

func snapshotCommands(t *testing.T, conn *sql.DB) string {
	t.Helper()
	rows, err := conn.Query(`SELECT id, device_id, command_type, command_version, idempotency_key, status,
		requested_at, leased_at, lease_until, lease_generation,
		accepted_at, running_at, finished_at, result_code, created_at, updated_at
		FROM device_control_commands ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sb strings.Builder
	cols := make([]any, 16)
	vals := make([]sql.NullString, 16)
	for i := range cols {
		cols[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(cols...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			sb.WriteString(v.String)
			sb.WriteByte('|')
		}
		sb.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var presence int
	if err := conn.QueryRow(`SELECT count(*) FROM device_control_presence`).Scan(&presence); err != nil {
		t.Fatal(err)
	}
	sb.WriteString("presence=")
	sb.WriteString(strconv.Itoa(presence))
	return sb.String()
}

// Existing invalid pending rows fail the upgrade safely with prior state
// preserved (rollback, no deletions, version stays 17).
func TestV17To18InvalidRowBlocked(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 17); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('88888888-8888-4888-8888-888888888888', 'shop', 'active', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO device_control_commands
		(id, device_id, command_type, command_version, idempotency_key, status, requested_at, lease_until, created_at, updated_at)
		VALUES ('99999999-9999-4999-8999-999999999999', '88888888-8888-4888-8888-888888888888',
			'sync_now', 1, 'invalid-lease', 'pending', now(), now(), now(), now())`); err != nil {
		t.Fatalf("vacuous 00017 CHECK allows the invalid row (proves F09): %v", err)
	}
	err := migrate.Up(ctx, conn)
	if err == nil {
		t.Fatal("upgrade must fail on the invalid row")
	}
	if !strings.Contains(err.Error(), "device_control_commands_pending_lease_null") {
		t.Fatalf("bounded diagnostic naming the constraint: %v", err)
	}
	if v := version(t, conn, ctx); v != 17 {
		t.Fatalf("failed upgrade rolls back to 17: %d", v)
	}
	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM device_control_commands WHERE id = '99999999-9999-4999-8999-999999999999'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid row preserved (no deletions): %d (%v)", count, err)
	}
}
