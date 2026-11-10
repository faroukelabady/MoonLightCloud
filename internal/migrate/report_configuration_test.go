package migrate_test

import (
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

func TestReportConfigurationUpgradePreservesExistingData(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 117); err != nil {
		t.Fatal(err)
	}
	// Seed exactly the previous writer's shape, before snapshot columns exist.
	_, err := conn.Exec(`
INSERT INTO notification_recipients(id,purpose,label,provider_key,recipient,locale)
VALUES ('11111111-1111-4111-8111-111111111111','business_report','Existing owner','telegram-main','123456789','ar');
INSERT INTO business_report_schedules(id,name,report_kind,timezone,local_time,next_run_local_date,next_run_at)
VALUES ('22222222-2222-4222-8222-222222222222','Existing daily','daily','Africa/Cairo','21:00',current_date,now());
INSERT INTO business_report_schedule_recipients(schedule_id,recipient_id)
VALUES ('22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111');
INSERT INTO business_report_runs(id,schedule_id,run_kind,manual_idempotency_key,scheduled_for,period_start,period_end,schedule_revision)
VALUES ('33333333-3333-4333-8333-333333333333','22222222-2222-4222-8222-222222222222','manual','existing-run',now(),now()-interval '1 day',now(),1);`)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := conn.QueryRow(`SELECT to_jsonb(r)::text FROM business_report_runs r`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var after, kind, name, address string
	if err := conn.QueryRow(`SELECT (to_jsonb(r)-'report_kind'-'schedule_name')::text,report_kind,schedule_name FROM business_report_runs r`).Scan(&after, &kind, &name); err != nil {
		t.Fatal(err)
	}
	if before != after || kind != "daily" || name != "Existing daily" {
		t.Fatal("upgrade changed existing run data or failed to backfill its identity")
	}
	if err := conn.QueryRow(`SELECT recipient FROM notification_recipients`).Scan(&address); err != nil || address != "123456789" {
		t.Fatal("upgrade changed recipient")
	}
	// A safe rollback and re-upgrade retain the same original rows.
	if err := migrate.DownTo(ctx, conn, 117); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	// A previous-version insert still gets a snapshot through the trigger.
	if _, err := conn.Exec(`INSERT INTO business_report_runs(id,schedule_id,run_kind,manual_idempotency_key,scheduled_for,period_start,period_end,schedule_revision)
VALUES ('44444444-4444-4444-8444-444444444444','22222222-2222-4222-8222-222222222222','manual','previous-writer',now(),now()-interval '1 day',now(),1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE notification_recipients SET deleted_at=now(),enabled=false`); err != nil {
		t.Fatal(err)
	}
	err = migrate.DownTo(ctx, conn, 117)
	if err == nil || !strings.Contains(err.Error(), "would discard retained configuration or history") {
		t.Fatalf("unsafe downgrade was not refused: %v", err)
	}
	if version(t, conn, ctx) != 118 {
		t.Fatal("refused downgrade changed schema version")
	}
	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM business_report_runs`).Scan(&count); err != nil || count != 2 {
		t.Fatal("downgrade refusal lost history")
	}
}
