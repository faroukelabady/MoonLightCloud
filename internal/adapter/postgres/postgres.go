// Package postgres implements repository contracts with pgx v5 + sqlc.
// Business packages must not import pgx directly. Generated query code lives
// in sqlcgen and is never hand-edited.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/MoonLightSoftware/MoonLightCloud/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open creates the pgxpool with small-deployment defaults. Callers pass
// explicit query timeouts per operation; Close shuts the pool down.
func Open(ctx context.Context, c config.Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	pc.MaxConns = c.DBMaxConns
	pc.MinConns = c.DBMinConns
	pc.MaxConnLifetime = c.DBMaxConnLife
	pc.MaxConnIdleTime = c.DBMaxConnIdle
	pc.ConnConfig.ConnectTimeout = c.DBConnectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, c.DBConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// QueryTimeout bounds a single database operation.
func QueryTimeout(c config.Config) time.Duration { return c.DBQueryTimeout }
