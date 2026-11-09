package migrate_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
)

const schemaGolden = "testdata/schema.golden"

// The migrated schema must equal the reviewed target (ADR-0055 Phase 3). Any
// schema change shows up as a diff of the golden file in review.
// Regenerate after a reviewed change with:
//
//	MOONLIGHT_REGEN_SCHEMA=1 go test ./internal/migrate -run TestSchemaMatchesReviewedTarget
func TestSchemaMatchesReviewedTarget(t *testing.T) {
	conn, err := sql.Open("pgx", testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var b strings.Builder
	section := func(title, query string) {
		t.Helper()
		rows, err := conn.Query(query)
		if err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		defer rows.Close()
		b.WriteString("-- " + title + "\n")
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			b.WriteString(line + "\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		b.WriteString("\n")
	}
	section("columns", `SELECT table_name || '.' || column_name || ' ' || udt_name ||
			CASE WHEN is_nullable = 'NO' THEN ' NOT NULL' ELSE '' END ||
			COALESCE(' DEFAULT ' || column_default, '')
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		ORDER BY table_name, ordinal_position`)
	section("constraints", `SELECT c.conrelid::regclass::text || ' ' || c.conname || ' ' || pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		WHERE c.connamespace = 'public'::regnamespace AND c.conrelid <> 0
		  AND c.conrelid::regclass::text <> 'goose_db_version'
		ORDER BY c.conrelid::regclass::text, c.conname`)
	section("indexes", `SELECT indexdef FROM pg_indexes
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'
		ORDER BY tablename, indexname`)
	section("views", `SELECT viewname || ': ' || regexp_replace(definition, '\s+', ' ', 'g')
		FROM pg_views WHERE schemaname = 'public' ORDER BY viewname`)
	section("functions", `SELECT p.proname || ': ' || btrim(regexp_replace(pg_get_functiondef(p.oid), '\s+', ' ', 'g'))
		FROM pg_proc p WHERE p.pronamespace = 'public'::regnamespace ORDER BY p.proname`)
	section("triggers", `SELECT pg_get_triggerdef(t.oid) FROM pg_trigger t
		WHERE NOT t.tgisinternal ORDER BY t.tgrelid::regclass::text, t.tgname`)
	got := b.String()

	if os.Getenv("MOONLIGHT_REGEN_SCHEMA") == "1" {
		if err := os.MkdirAll(filepath.Dir(schemaGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(schemaGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(schemaGolden)
	if err != nil {
		t.Fatalf("read %s (run with MOONLIGHT_REGEN_SCHEMA=1): %v", schemaGolden, err)
	}
	if got != string(want) {
		t.Fatalf("migrated schema differs from %s; review the change and regenerate with MOONLIGHT_REGEN_SCHEMA=1", schemaGolden)
	}
}
