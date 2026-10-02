package migrate_test

import (
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

func TestFreshTo26RecipientBounds(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if got := version(t, conn, ctx); got != 26 {
		t.Fatalf("schema=%d want=26", got)
	}
	for _, constraint := range []string{"notification_messages_recipient_check", "business_report_recipients_recipient_check", "operational_alert_recipients_recipient_check"} {
		var definition string
		if err := conn.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname=$1`, constraint).Scan(&definition); err != nil || !strings.Contains(definition, "33") {
			t.Fatalf("constraint=%s definition=%s %v", constraint, definition, err)
		}
	}
	for _, table := range []string{"business_report_deliveries", "operational_alert_deliveries"} {
		var typ string
		var maximum *int
		if err := conn.QueryRow(`SELECT data_type,character_maximum_length FROM information_schema.columns WHERE table_name=$1 AND column_name='recipient_snapshot'`, table).Scan(&typ, &maximum); err != nil || typ != "text" || maximum != nil {
			t.Fatalf("snapshot bound %s: %s %v %v", table, typ, maximum, err)
		}
	}
}
