package migrate_test

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// v16 → 17 preserves Phase 7B state exactly and adds control tables.
func TestV16To17PreservesReports(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 16); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 16 {
		t.Fatalf("want 16, got %d", v)
	}
	if _, err := conn.Exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`); err != nil {
		t.Fatalf("seed device at v16: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO business_report_recipients (id, label, provider_key, recipient, locale)
		VALUES ('22222222-2222-4222-8222-222222222222', 'owner', 'whatsapp-main', '201012345678', 'ar')`); err != nil {
		t.Fatalf("seed recipient at v16: %v", err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var label string
	if err := conn.QueryRow(`SELECT label FROM business_report_recipients
		WHERE id='22222222-2222-4222-8222-222222222222'`).Scan(&label); err != nil || label != "owner" {
		t.Fatalf("7B recipient preserved: %q (%v)", label, err)
	}
	for _, table := range []string{"device_control_presence", "device_control_commands"} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing (%v)", table, err)
		}
	}
	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM device_control_commands`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("commands start empty: %d (%v)", count, err)
	}
}
