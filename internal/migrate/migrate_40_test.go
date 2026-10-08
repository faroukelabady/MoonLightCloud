package migrate_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

func seedMigration40Entity(t *testing.T, conn *sql.DB, ctx context.Context) {
	t.Helper()
	queries := []string{
		`INSERT INTO devices(id,name,status) VALUES ('11111111-1111-4111-8111-111111111111','migration-test','active')`,
		`INSERT INTO stores(id,display_name,timezone) VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','Migration test','Africa/Cairo')`,
		`INSERT INTO catalog_admin_commands(id,store_id,command_type,command_version,entity_id,payload,payload_hash,expected_revision,actor)
		 VALUES ('22222222-2222-4222-8222-222222222222','aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','catalog.product.details.update.v1',1,
		 '33333333-3333-4333-8333-333333333333','{"name_ar":"محفوظ"}','0000000000000000000000000000000000000000000000000000000000000000',7,'operator')`,
		`INSERT INTO catalog_admin_command_targets(id,command_id,device_id,status,entity_id)
		 VALUES ('44444444-4444-4444-8444-444444444444','22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111','PENDING','33333333-3333-4333-8333-333333333333')`,
	}
	for _, query := range queries {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
}

func migration40State(t *testing.T, conn *sql.DB, ctx context.Context) string {
	t.Helper()
	var state string
	if err := conn.QueryRowContext(ctx, `SELECT jsonb_build_object(
		'commands',(SELECT jsonb_agg(to_jsonb(c)-'target_kind'-'requested_key'-'result_entity_id' ORDER BY id) FROM catalog_admin_commands c),
		'targets',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM catalog_admin_command_targets t)
	)::text`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestMigration40RollbackReapplyPreservesEntityState(t *testing.T) {
	for _, useUp := range []bool{false, true} {
		t.Run(map[bool]string{false: "UpTo", true: "Up"}[useUp], func(t *testing.T) {
			conn, ctx := openRaw(t)
			if err := migrate.UpTo(ctx, conn, 39); err != nil {
				t.Fatal(err)
			}
			seedMigration40Entity(t, conn, ctx)
			before := migration40State(t, conn, ctx)
			// Up goes to the latest schema (Phase 18 added 41); UpTo stops at 40.
			want := int64(40)
			if useUp {
				want = migrate.TargetVersion
			}
			upgrade := func() error {
				if useUp {
					return migrate.Up(ctx, conn)
				}
				return migrate.UpTo(ctx, conn, 40)
			}
			for i := 0; i < 2; i++ {
				if err := upgrade(); err != nil {
					t.Fatal(err)
				}
				if version(t, conn, ctx) != want || migration40State(t, conn, ctx) != before {
					t.Fatal("upgrade changed entity command or target state")
				}
				if err := migrate.DownTo(ctx, conn, 39); err != nil {
					t.Fatal(err)
				}
				if version(t, conn, ctx) != 39 || migration40State(t, conn, ctx) != before {
					t.Fatal("rollback changed entity command or target state")
				}
				// The shipped Down shape retains the strict original bound.
				if _, err := conn.ExecContext(ctx, `UPDATE catalog_admin_commands SET entity_id=''`); err == nil {
					t.Fatal("empty entity accepted after rollback")
				}
				if _, err := conn.ExecContext(ctx, `UPDATE catalog_admin_commands SET entity_id=repeat('x',65)`); err == nil {
					t.Fatal("overlong entity accepted after rollback")
				}
			}
			if err := upgrade(); err != nil {
				t.Fatal(err)
			}
			// Latest-version metadata wins even when an older row was inserted last.
			if _, err := conn.ExecContext(ctx, `INSERT INTO goose_db_version(version_id,is_applied) VALUES (34,true)`); err != nil {
				t.Fatal(err)
			}
			if err := upgrade(); err != nil || version(t, conn, ctx) != want {
				t.Fatalf("out-of-order goose metadata: %v", err)
			}
		})
	}
}

func TestMigration40RollbackNormalizationRejectsUnexpectedShapes(t *testing.T) {
	for _, tc := range []struct {
		name, change string
	}{
		{"weakened", `ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_entity_check; ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_entity_check CHECK(char_length(entity_id) BETWEEN 0 AND 128)`},
		{"not_validated", `ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_entity_check; ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_entity_check CHECK(char_length(entity_id) BETWEEN 1 AND 64) NOT VALID`},
		{"ambiguous_names", `ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_entity_id_check CHECK(char_length(entity_id) BETWEEN 1 AND 64)`},
		{"missing", `ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_entity_check`},
		{"partial_new_columns", `ALTER TABLE catalog_admin_commands ADD COLUMN requested_key text`},
		{"nullable_entity", `ALTER TABLE catalog_admin_commands ALTER COLUMN entity_id DROP NOT NULL`},
		{"extra_entity_check", `ALTER TABLE catalog_admin_commands ADD CONSTRAINT unknown_entity_check CHECK(char_length(entity_id) <= 64)`},
		{"unknown_name", `ALTER TABLE catalog_admin_commands RENAME CONSTRAINT catalog_admin_commands_entity_check TO unknown_entity_check`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, ctx := openRaw(t)
			if err := migrate.UpTo(ctx, conn, 39); err != nil {
				t.Fatal(err)
			}
			seedMigration40Entity(t, conn, ctx)
			if err := migrate.Up(ctx, conn); err != nil {
				t.Fatal(err)
			}
			if err := migrate.DownTo(ctx, conn, 39); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(ctx, tc.change); err != nil {
				t.Fatal(err)
			}
			before := migration40State(t, conn, ctx)
			constraints := func() string {
				var result string
				if err := conn.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_array(conname,pg_get_constraintdef(oid)) ORDER BY conname)::text,'[]') FROM pg_constraint WHERE conrelid='catalog_admin_commands'::regclass`).Scan(&result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			checks := constraints()
			err := migrate.UpTo(ctx, conn, 40)
			if err == nil || !strings.HasPrefix(err.Error(), "migration40 compatibility: unexpected schema 39") || len(err.Error()) > 100 {
				t.Fatalf("unexpected shape must fail with bounded diagnostic: %v", err)
			}
			if version(t, conn, ctx) != 39 || migration40State(t, conn, ctx) != before || constraints() != checks {
				t.Fatal("rejection changed durable state or check definitions")
			}
		})
	}
}

func TestMigration40DeeperRollbackReapplyPreservesEntityState(t *testing.T) {
	for _, useUp := range []bool{false, true} {
		t.Run(map[bool]string{false: "UpTo", true: "Up"}[useUp], func(t *testing.T) {
			conn, ctx := openRaw(t)
			if err := migrate.UpTo(ctx, conn, 38); err != nil {
				t.Fatal(err)
			}
			seedMigration40Entity(t, conn, ctx)
			before := migration40State(t, conn, ctx)
			if err := migrate.Up(ctx, conn); err != nil {
				t.Fatal(err)
			}
			if err := migrate.DownTo(ctx, conn, 38); err != nil {
				t.Fatal(err)
			}
			if version(t, conn, ctx) != 38 || migration40State(t, conn, ctx) != before {
				t.Fatal("deep rollback changed entity command or target")
			}
			var err error
			if useUp {
				err = migrate.Up(ctx, conn)
			} else {
				err = migrate.UpTo(ctx, conn, 40)
			}
			want := int64(40)
			if useUp {
				want = migrate.TargetVersion
			}
			if err != nil || version(t, conn, ctx) != want || migration40State(t, conn, ctx) != before {
				t.Fatalf("deep rollback reapply must preserve state: %v", err)
			}
		})
	}
}
