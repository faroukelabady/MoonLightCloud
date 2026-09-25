// Package app wires config, logger, pool, repositories, services, and the
// HTTP server. Startup is deterministic: config, logger, pool, connectivity,
// schema compatibility, services, server. No silent auto-migration.
package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	adapterhttp "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/logging"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/faroukelabady/MoonLightCloud/internal/sync"
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
	Cfg             config.Config
	Log             *slog.Logger
	Pool            *pgxpool.Pool
	Devices         auth.Service
	Sync            sync.Service
	Reports         report.Service
	Dashboard       dashboard.Service
	Projector       *sale.Projector
	SaleStore       sale.Store
	ReturnProjector *returnrefund.Projector
	ReturnStore     returnrefund.Store
	Handler         http.Handler
	Health          adapterhttp.Health
	Version         adapterhttp.Version
}

// New builds the app in startup order.
func New(ctx context.Context, cfg config.Config) (*App, error) {
	log := logging.New(cfg.LogLevel)
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	hasher, err := auth.NewHasher(cfg.Pepper)
	if err != nil {
		pool.Close()
		return nil, err
	}
	store := postgres.NewDevices(pool, config.DefaultDBQueryTimeout)
	a := &App{Cfg: cfg, Log: log, Pool: pool}
	a.Devices = auth.NewService(
		store, hasher, cfg.PepperRaw, cfg.PepperVersion,
		clock.System{}, ids.System{},
	)
	a.Sync = sync.NewService(store, clock.System{})
	sync.RegisterEventType(sale.EventSaleFinalizedV1, ValidateSalePayload)
	sync.RegisterEventType(returnrefund.EventReturnRefundFinalizedV1, ValidateReturnRefundPayload)
	a.SaleStore = store
	a.Projector = sale.NewProjector(store, clock.System{}, log)
	a.ReturnStore = store
	a.ReturnProjector = returnrefund.NewProjector(store, clock.System{}, log)
	a.Reports = report.NewService(store, clock.System{}, cfg.StoreLocation)
	a.Health = adapterhttp.Health{
		LiveCheck:  func() bool { return true },
		ReadyCheck: a.checkReady,
	}
	a.Version = adapterhttp.Version{App: AppName, Version: Version, Commit: Commit, BuildTime: BuildTime}
	a.Dashboard = dashboard.NewService(a.Reports, store, store, clock.System{})
	sessionKey, err := dashboard.SessionKey(cfg.Pepper)
	if err != nil {
		pool.Close()
		return nil, err
	}
	secureCookies := cfg.Environment == config.EnvProduction
	dashAuth := adapterhttp.NewDashboardHandlers(
		dashboard.Credentials{Username: cfg.DashboardUsername, PasswordHash: cfg.DashboardPasswordHash},
		sessionKey, cfg.DashboardSessionTTL, secureCookies, cfg.TrustedProxyCIDRs, log)
	dashData := adapterhttp.NewDashboardDataHandlers(a.Dashboard, a.Reports, log)
	a.Handler = adapterhttp.Router(log, a.Health, a.Version, a.Devices, a.Sync, a.notifyProjectors,
		adapterhttp.NewReportHandlers(a.Reports, log), cfg.ReportingToken,
		dashAuth, dashData, cfg.DashboardAssetsDir)
	if err := a.VerifySchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return a, nil
}

// ValidateSalePayload is the ingestion-time sale.finalized.v1 gate: decode
// the canonical payload, run the strict validator. One authoritative
// Decode+Validate shared with the projector's defensive re-check.
func ValidateSalePayload(raw json.RawMessage) error {
	p, err := sale.Decode(raw)
	if err != nil {
		return err
	}
	_, err = sale.Validate(p)
	return err
}

// ValidateReturnRefundPayload is the ingestion-time
// sale.return_refund.finalized.v1 gate: event-local validation only, before
// durable ACK. No projection exists in Phase 4A; cumulative business
// validation belongs to Phase 4B.
func ValidateReturnRefundPayload(raw json.RawMessage) error {
	p, err := returnrefund.Decode(raw)
	if err != nil {
		return err
	}
	_, err = returnrefund.Validate(p)
	return err
}

// ProjectOne loads one inbox event and runs a single atomic projection
// attempt. Used by tests and operator tooling; serve-path projection goes
// through the background Projector.
func (a *App) ProjectOne(ctx context.Context, eventID string) (sale.ProjectResult, error) {
	rec, ok, err := a.SaleStore.LoadSaleEvent(ctx, eventID)
	if err != nil {
		return sale.ProjectResult{}, err
	}
	if !ok {
		return sale.ProjectResult{}, fmt.Errorf("event %s not found", eventID)
	}
	return a.SaleStore.ProjectSale(ctx, rec, clock.System{}.Now())
}

// notifyProjectors wakes both projectors after a durable ingestion. Scan
// work is authoritative per processor, so one wake each is sufficient and
// neither projector starves the other.
func (a *App) notifyProjectors() {
	a.Projector.Notify()
	a.ReturnProjector.Notify()
}

// ProjectReturnOne loads one return inbox event and runs a single atomic
// projection attempt. Used by tests and operator tooling; serve-path
// projection goes through the background ReturnProjector.
func (a *App) ProjectReturnOne(ctx context.Context, eventID string) (returnrefund.ProjectResult, error) {
	rec, ok, err := a.ReturnStore.LoadReturnEvent(ctx, eventID)
	if err != nil {
		return returnrefund.ProjectResult{}, err
	}
	if !ok {
		return returnrefund.ProjectResult{}, fmt.Errorf("event %s not found", eventID)
	}
	return a.ReturnStore.ProjectReturn(ctx, rec, clock.System{}.Now())
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
