// Package db owns the SQL migration history and query sources.
// Go code never hand-edits generated output; see sqlc.yaml and generate.sh.
package db

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var raw embed.FS

// Migrations is the migration file set rooted at migrations/ so the goose
// provider sees versioned *.sql files directly.
func Migrations() fs.FS {
	sub, err := fs.Sub(raw, "migrations")
	if err != nil {
		panic("db migrations: " + err.Error())
	}
	return sub
}
