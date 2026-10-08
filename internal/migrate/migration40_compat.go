package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pressly/goose/v3"
)

// Shipped 00040 Down restores the v39 check under a different name from
// 00032. Rename only that proven equivalent check before replaying 00040 Up;
// never drop a check, rewrite data or alter the embedded migration history.
func normalizeMigration40Rollback(ctx context.Context, conn *sql.DB, p *goose.Provider) error {
	version, err := p.GetDBVersion(ctx)
	if err != nil {
		return errors.New("migration40 compatibility: cannot inspect schema version")
	}
	// A rollback can proceed below 39 without changing the restored entity
	// check's name. Reach the exact v39 boundary before inspecting that shape.
	if version < 39 {
		if _, err := p.UpTo(ctx, 39); err != nil {
			// Preserve existing migration diagnostics from earlier versions;
			// the bounded compatibility errors below apply to our own repair.
			return fmt.Errorf("goose up-to schema 39: %w", err)
		}
		version = 39
	}
	if version != 39 {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("migration40 compatibility: cannot begin inspection")
	}
	defer tx.Rollback() //nolint:errcheck // committed transactions are already closed
	if _, err := tx.ExecContext(ctx, `LOCK TABLE goose_db_version, catalog_admin_commands IN ACCESS EXCLUSIVE MODE`); err != nil {
		return errors.New("migration40 compatibility: cannot lock schema")
	}
	// Goose uses the maximum recorded version, not the last inserted row:
	// out-of-order application must not make a newer schema look like v39.
	if err := tx.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil {
		return errors.New("migration40 compatibility: cannot recheck schema version")
	}
	if version != 39 {
		return errors.New("migration40 compatibility: schema version changed")
	}
	var newColumns bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_attribute WHERE attrelid='catalog_admin_commands'::regclass
		AND attname IN ('target_kind','requested_key','result_entity_id') AND NOT attisdropped
	)`).Scan(&newColumns); err != nil {
		return errors.New("migration40 compatibility: cannot inspect command columns")
	}
	if newColumns {
		return errors.New("migration40 compatibility: unexpected schema 39 columns")
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.conname, pg_get_constraintdef(c.oid),
		c.contype='c' AND c.convalidated AND c.conislocal AND c.coninhcount=0 AND NOT c.connoinherit
		AND a.attnotnull AND a.atttypid='text'::regtype AND a.attgenerated='' AND a.attidentity=''
		AND c.conkey=ARRAY[a.attnum]
		FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attname='entity_id'
		WHERE c.conrelid='catalog_admin_commands'::regclass
		AND c.contype<>'n'
		AND c.conkey @> ARRAY[a.attnum]`)
	if err != nil {
		return errors.New("migration40 compatibility: cannot inspect entity check")
	}
	var name, definition string
	var valid bool
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&name, &definition, &valid); err != nil {
			rows.Close()
			return errors.New("migration40 compatibility: cannot read entity check")
		}
	}
	readErr := rows.Err()
	rows.Close()
	// PostgreSQL deparses both shipped constraints to the same expression.
	// Ignore presentation whitespace/parentheses only, not operators or literals.
	shape := strings.NewReplacer("(", "", ")", "").Replace(strings.Join(strings.Fields(definition), ""))
	knownName := name == "catalog_admin_commands_entity_id_check" || name == "catalog_admin_commands_entity_check"
	if readErr != nil || count != 1 || !knownName || !valid || shape != "CHECKchar_lengthentity_id>=1ANDchar_lengthentity_id<=64" {
		return errors.New("migration40 compatibility: unexpected schema 39 entity check")
	}
	if name == "catalog_admin_commands_entity_check" {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE catalog_admin_commands RENAME CONSTRAINT catalog_admin_commands_entity_check TO catalog_admin_commands_entity_id_check`); err != nil {
			return errors.New("migration40 compatibility: cannot normalize entity check name")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("migration40 compatibility: cannot commit inspection")
	}
	return nil
}
