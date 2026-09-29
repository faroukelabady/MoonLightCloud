package migrate_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// Fresh database reaches 20 with operations tables and the carried
// pending/lease constraint enforced.
func TestFreshTo19(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 20 {
		t.Fatalf("want 20, got %d", v)
	}
	for _, table := range []string{
		"operational_alert_recipients", "operational_incidents",
		"operational_alert_deliveries", "operational_recovery_actions",
	} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing (%v)", table, err)
		}
	}
	var enforced bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'device_control_commands_pending_lease_null')`).Scan(&enforced); err != nil || !enforced {
		t.Fatalf("carried constraint enforced: %v", err)
	}
}

// v18 → v19 preserves every frozen row the 7D detector reads. Migration
// 00019 adds tables only; existing state must be byte-identical after.
func TestV18To19Preservation(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 18); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`)
	exec(`INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`)
	mkCmd := func(id, status, extraCols, extraVals string) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO device_control_commands
			(id, device_id, command_type, command_version, idempotency_key, status, requested_at, created_at, updated_at` + extraCols + `)
			VALUES ('` + id + `', '11111111-1111-4111-8111-111111111111', 'sync_now', 1, 'k-` + id[:8] + `', '` + status + `', now(), now(), now()` + extraVals + `)`); err != nil {
			t.Fatalf("seed %s: %v", status, err)
		}
	}
	// NOTE: one-active invariant — these share a device across separate
	// terminal/non-terminal rows only where the index allows: use distinct
	// devices per non-terminal command.
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('22222222-2222-4222-8222-222222222222', 'back', 'active', now(), now())`)
	mkCmd("33333333-3333-4333-8333-333333333333", "pending", "", "")
	exec(`INSERT INTO device_control_commands (id, device_id, command_type, command_version, idempotency_key, status, requested_at, finished_at, result_code, created_at, updated_at)
		VALUES ('44444444-4444-4433-8444-444444444444', '22222222-2222-4222-8222-222222222222', 'sync_now', 1, 'k-term', 'failed', now(), now(), 'SYNC_FAILED_NETWORK', now(), now())`)
	exec(`INSERT INTO notification_template_mappings
		(provider_key, template_key, locale, external_template_name, external_language_code, parameter_names, enabled)
		VALUES ('whatsapp-main', 'daily_business_report_v1', 'ar', 'ext', 'ar', '{report_body}', TRUE)`)
	exec(`INSERT INTO notification_messages
		(id, provider_key, idempotency_key, semantic_fingerprint, recipient, template_key, locale,
		 ext_template_name, ext_language_code, dispatch_status, delivery_status)
		VALUES ('55555555-5555-4533-8555-555555555555', 'whatsapp-main', 'nb-1', '\x01',
			'201012345678', 'daily_business_report_v1', 'ar', 'ext', 'ar', 'blocked', 'UNKNOWN')`)
	exec(`INSERT INTO business_report_recipients (id, label, provider_key, recipient, locale)
		VALUES ('66666666-6666-4633-8666-666666666666', 'owner', 'whatsapp-main', '201012345678', 'ar')`)
	exec(`INSERT INTO business_report_schedules (id, name, report_kind, timezone, local_time, enabled, revision, next_run_local_date, next_run_at, created_at, updated_at)
		VALUES ('77777777-7777-4733-8777-777777777777', 'daily', 'DAILY', 'Africa/Cairo', '08:00', TRUE, 1, CURRENT_DATE, now(), now(), now())`)
	exec(`INSERT INTO business_report_runs (id, schedule_id, run_kind, slot_local_date, scheduled_for, period_start, period_end, schedule_revision, status, created_at, updated_at)
		VALUES ('88888888-8888-4888-8888-888888888888', '77777777-7777-4733-8777-777777777777', 'scheduled', CURRENT_DATE - 1, now(), now(), now(), 1, 'blocked', now(), now())`)
	beforeCmds := dumpTable(t, conn, `SELECT id, device_id, status, result_code FROM device_control_commands ORDER BY id`)
	beforeNotes := dumpTable(t, conn, `SELECT id, dispatch_status FROM notification_messages ORDER BY id`)
	beforeRuns := dumpTable(t, conn, `SELECT id, status FROM business_report_runs ORDER BY id`)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 20 {
		t.Fatalf("want 20, got %d", v)
	}
	var enforced bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='operational_deliveries_identity_unique')`).Scan(&enforced); err != nil || !enforced {
		t.Fatalf("delivery identity unique present (%v)", err)
	}
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname='idx_operations_incidents_resolve')`).Scan(&enforced); err != nil || !enforced {
		t.Fatalf("resolve index present (%v)", err)
	}
	if got := dumpTable(t, conn, `SELECT id, device_id, status, result_code FROM device_control_commands ORDER BY id`); got != beforeCmds {
		t.Fatalf("commands preserved:\n%s\nvs\n%s", beforeCmds, got)
	}
	if got := dumpTable(t, conn, `SELECT id, dispatch_status FROM notification_messages ORDER BY id`); got != beforeNotes {
		t.Fatalf("notifications preserved:\n%s\nvs\n%s", beforeNotes, got)
	}
	if got := dumpTable(t, conn, `SELECT id, status FROM business_report_runs ORDER BY id`); got != beforeRuns {
		t.Fatalf("runs preserved:\n%s\nvs\n%s", beforeRuns, got)
	}
	for _, table := range []string{"sync_events", "sales_projection", "devices", "notification_messages", "business_report_runs", "device_control_commands", "device_control_presence"} {
		var n int
		if err := conn.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("table %s readable: %v", table, err)
		}
	}
}

func dumpTable(t *testing.T, conn *sql.DB, q string) string {
	t.Helper()
	rows, err := conn.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
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
	return sb.String()
}

// Rollback 20 → 18 leaves no Phase 7D indexes or tables, preserves frozen
// rows, and re-upgrade restores full behavior.
func TestRollback20To18Clean(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 18); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 18 {
		t.Fatalf("want 18, got %d", v)
	}
	for _, table := range []string{
		"operational_alert_recipients", "operational_incidents",
		"operational_alert_deliveries", "operational_recovery_actions",
	} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || exists {
			t.Fatalf("table %s gone (%v)", table, err)
		}
	}
	for _, idx := range []string{
		"idx_operations_commands_failed", "idx_operations_commands_stale",
		"idx_operations_incidents_resolve",
	} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname=$1)`, idx).Scan(&exists); err != nil || exists {
			t.Fatalf("index %s gone (%v)", idx, err)
		}
	}
	var devices int
	if err := conn.QueryRow(`SELECT count(*) FROM devices`).Scan(&devices); err != nil || devices != 1 {
		t.Fatalf("frozen rows preserved: %d (%v)", devices, err)
	}
	// Re-upgrade restores everything including the replacement stale index.
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 20 {
		t.Fatalf("want 20, got %d", v)
	}
	var enforced bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname='idx_operations_commands_stale')`).Scan(&enforced); err != nil || !enforced {
		t.Fatalf("stale index restored (%v)", err)
	}
}
