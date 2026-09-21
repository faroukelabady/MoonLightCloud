// Package migrate applies the embedded goose history explicitly
// (migrate.sh, CLI). The server never auto-migrates silently on startup;
// it only verifies schema compatibility.
//
// Migrations use the split up/down file layout (00001_name.up.sql) so sqlc
// can parse only the up files for schema. The provider API understands this
// layout; the legacy global goose functions do not.
package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/faroukelabady/MoonLightCloud/db"
	"github.com/pressly/goose/v3"
)

// TargetVersion is the schema version Phase 2B code requires.
const TargetVersion int64 = 5

func provider(conn *sql.DB) (*goose.Provider, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, conn, db.Migrations())
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	return p, nil
}

// Up applies pending migrations.
func Up(ctx context.Context, conn *sql.DB) error {
	p, err := provider(conn)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// UpTo applies migrations up to a version (migration-path tests).
func UpTo(ctx context.Context, conn *sql.DB, version int64) error {
	p, err := provider(conn)
	if err != nil {
		return err
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		return fmt.Errorf("goose up-to: %w", err)
	}
	return nil
}

// Current returns the applied version, or 0 for an empty database.
func Current(ctx context.Context, conn *sql.DB) (int64, error) {
	p, err := provider(conn)
	if err != nil {
		return 0, err
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("goose version: %w", err)
	}
	return v, nil
}
