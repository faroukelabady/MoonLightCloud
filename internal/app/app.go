// Package app wires config, logger, pool, repositories, services, and the
// HTTP server. Startup is deterministic: config, logger, pool, connectivity,
// schema compatibility, services, server. No silent auto-migration.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	adapterhttp "github.com/MoonLightSoftware/MoonLightCloud/internal/adapter/http"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/adapter/postgres"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/apperr"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/auth"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/config"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/migrate"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/platform/clock"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/platform/ids"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/platform/logging"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Build metadata injected at build time via ldflags.
var (
	AppName   = "moonlight-cloud"
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// App is the composed application.
type App struct {
	Cfg     config.Config
	Log     *slog.Logger
	Pool    *pgxpool.Pool
	Devices auth.Service
	Handler http.Handler
	Health  adapterhttp.Health
	Version adapterhttp.Version
}

// New builds the app in startup order.
func New(ctx context.Context, cfg config.Config) (*App, error) {
	log := logging.New(cfg.LogLevel)
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Log: log, Pool: pool}
	a.Devices = auth.NewService(
		postgres.NewDevices(pool, config.DefaultDBQueryTimeout),
		auth.NewHasher(cfg.SecretPepper),
		clock.System{},
		ids.System{},
	)
	a.Health = adapterhttp.Health{
		LiveCheck:  func() bool { return true },
		ReadyCheck: a.checkReady,
	}
	a.Version = adapterhttp.Version{App: AppName, Version: Version, Commit: Commit, BuildTime: BuildTime}
	a.Handler = adapterhttp.Router(log, a.Health, a.Version, a.Devices)
	if err := a.VerifySchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return a, nil
}

// VerifySchema fails startup when the database is not at TargetVersion.
// Migrations run explicitly via `migrate up`, never silently here.
func (a *App) VerifySchema(ctx context.Context) error {
	conn, err := sql.Open("pgx", a.Cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open sql for version check: %w", err)
	}
	defer conn.Close()
	v, err := migrate.Current(ctx, conn)
	if err != nil {
		return fmt.Errorf("schema check: %w", err)
	}
	if v != migrate.TargetVersion {
		return fmt.Errorf("schema version %d, want %d: run ./scripts/migrate.sh up", v, migrate.TargetVersion)
	}
	return nil
}

// checkReady validates DB connectivity plus schema compatibility with a
// bounded context. No expensive diagnostics on every call. Failures are
// classified Unavailable so the probe answers 503, never 500 with internals.
func (a *App) checkReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := a.Pool.Ping(ctx); err != nil {
		return apperr.New(apperr.Unavailable, "postgres unreachable")
	}
	var regclass *string
	if err := a.Pool.QueryRow(ctx, "SELECT to_regclass('public.devices')::text").Scan(&regclass); err != nil {
		return apperr.New(apperr.Unavailable, "schema check failed")
	}
	if regclass == nil {
		return apperr.New(apperr.Unavailable, "schema not ready")
	}
	return nil
}

// Close shuts the pool down gracefully.
func (a *App) Close() { a.Pool.Close() }
