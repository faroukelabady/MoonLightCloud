// Package app wires config, logger, pool, repositories, services, and the
// HTTP server. Startup is deterministic: config, logger, pool, connectivity,
// schema compatibility, services, server. No silent auto-migration.
package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	adapterhttp "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/woocommerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/whatsapp"
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
	Cfg                    config.Config
	Log                    *slog.Logger
	Pool                   *pgxpool.Pool
	Devices                auth.Service
	Sync                   sync.Service
	Reports                report.Service
	Dashboard              dashboard.Service
	Projector              *sale.Projector
	SaleStore              sale.Store
	ReturnProjector        *returnrefund.Projector
	ReturnStore            returnrefund.Store
	CatalogStore           catalog.Store
	CategoryProjector      *catalog.Projector
	TagProjector           *catalog.Projector
	ProductProjector       *catalog.Projector
	PolicyProjector        *catalog.Projector
	InventoryProjector     *catalog.Projector
	Catalog                catalog.Service
	CommerceRegistry       *commerce.Registry
	OrderService           *orders.OrderService
	OrderProcessor         *orders.Processor
	NotificationRegistry   *notifications.Registry
	NotificationDispatcher *notifications.Dispatcher
	ReportService          *businessreports.Service
	ReportPlanner          *businessreports.Planner
	ReportRunner           *businessreports.Runner
	DeviceControl          *devicecontrol.Service
	Handler                http.Handler
	Health                 adapterhttp.Health
	Version                adapterhttp.Version
}

// authDeviceStatus adapts the existing device lifecycle to the control
// plane: only active devices may receive Sync Now commands. Revocation
// semantics stay owned by auth.
type authDeviceStatus struct {
	svc auth.Service
}

func (a authDeviceStatus) IsActive(ctx context.Context, deviceID string) (bool, error) {
	dev, err := a.svc.Get(ctx, deviceID)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Kind == apperr.NotFound {
			return false, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND")
		}
		return false, err
	}
	return dev.Status == auth.StatusActive, nil
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
	sync.RegisterEventType(catalog.EventCategorySnapshotV1, ValidateCatalogCategoryPayload)
	sync.RegisterEventType(catalog.EventTagSnapshotV1, ValidateCatalogTagPayload)
	sync.RegisterEventType(catalog.EventProductSnapshotV1, ValidateCatalogProductPayload)
	sync.RegisterEventType(catalog.EventProductSalesPolicySnapshotV1, ValidateCatalogProductSalesPolicyPayload)
	sync.RegisterEventType(catalog.EventInventoryProductSnapshotV1, ValidateCatalogProductInventoryPayload)
	a.SaleStore = store
	a.Projector = sale.NewProjector(store, clock.System{}, log)
	a.ReturnStore = store
	a.ReturnProjector = returnrefund.NewProjector(store, clock.System{}, log)
	a.CatalogStore = store
	a.CategoryProjector = catalog.NewCategoryProjector(store, clock.System{}, log)
	a.TagProjector = catalog.NewTagProjector(store, clock.System{}, log)
	a.ProductProjector = catalog.NewProductProjector(store, clock.System{}, log)
	a.PolicyProjector = catalog.NewProductSalesPolicyProjector(store, clock.System{}, log)
	a.InventoryProjector = catalog.NewProductInventoryProjector(store, clock.System{}, log)
	a.Catalog = catalog.NewService(store)
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
	dashOrders := adapterhttp.NewDashboardOrderHandlers(store, log)
	a.CommerceRegistry = commerce.NewRegistry()
	var commerceWebhooks *adapterhttp.CommerceWebhookHandlers
	if cfg.WooCommerce.Enabled {
		provider, err := woocommerce.NewWooCommerceProvider(cfg.WooCommerce, nil)
		if err != nil {
			pool.Close()
			return nil, err
		}
		if err := a.CommerceRegistry.Register(provider.Key(), provider); err != nil {
			pool.Close()
			return nil, err
		}
		a.OrderService = orders.NewOrderService(a.CommerceRegistry, store, log)
		if cfg.WooCommerce.OrdersEnabled {
			a.OrderProcessor = orders.NewProcessor(store, a.OrderService, orders.SystemClock{}, commerceOwner(), log)
			secret := cfg.WooCommerce.WebhookSecret
			key := string(provider.Key())
			commerceWebhooks = adapterhttp.NewCommerceWebhookHandlers(store,
				func(providerKey string) (string, bool) {
					if providerKey == key {
						return secret, true
					}
					return "", false
				}, a.OrderProcessor.Notify, log)
		}
	}
	a.Log.Info("commerce providers", "count", a.CommerceRegistry.Count(),
		"orders", cfg.WooCommerce.OrdersEnabled)
	a.NotificationRegistry = notifications.NewRegistry()
	var notificationWebhooks *adapterhttp.WhatsAppWebhookHandlers
	if cfg.WhatsAppNotifications.Enabled {
		provider, err := whatsapp.NewProvider(cfg.WhatsAppNotifications, nil)
		if err != nil {
			pool.Close()
			return nil, err
		}
		if err := a.NotificationRegistry.Register(provider.Key(), provider); err != nil {
			pool.Close()
			return nil, err
		}
		a.NotificationDispatcher = notifications.NewDispatcher(
			store, a.NotificationRegistry, notifications.SystemClock{}, commerceOwner(), log)
		secret := cfg.WhatsAppNotifications.WebhookVerifyToken
		appSecret := cfg.WhatsAppNotifications.AppSecret
		key := string(provider.Key())
		notificationWebhooks = adapterhttp.NewWhatsAppWebhookHandlers(store,
			func(providerKey string) (string, bool) {
				if providerKey == key {
					return secret, true
				}
				return "", false
			},
			func(providerKey string) (string, bool) {
				if providerKey == key {
					return appSecret, true
				}
				return "", false
			}, log)
	}
	a.Log.Info("notification providers", "count", a.NotificationRegistry.Count(),
		"whatsapp", cfg.WhatsAppNotifications.Enabled)
	if cfg.BusinessReports.Enabled {
		loc, err := businessreports.LoadCairo()
		if err != nil {
			pool.Close()
			return nil, err
		}
		notificationService := notifications.NewService(store, store, log)
		a.ReportService = businessreports.NewService(store, store, store, store,
			a.Reports, notificationService,
			businessreports.SystemClock{}, loc,
			cfg.BusinessReports.LeaseDuration, cfg.BusinessReports.BatchSize, log)
		a.ReportPlanner = businessreports.NewPlanner(store,
			businessreports.SystemClock{}, loc,
			cfg.BusinessReports.PollInterval, cfg.BusinessReports.BatchSize, log)
		a.ReportRunner = businessreports.NewRunner(store, store, a.Reports, notificationService,
			businessreports.SystemClock{}, loc, cfg.BusinessReports.PollInterval, 25,
			commerceOwner(), cfg.BusinessReports.LeaseDuration, log)
	}
	a.Log.Info("business reports", "enabled", cfg.BusinessReports.Enabled)
	a.DeviceControl = devicecontrol.NewService(store, authDeviceStatus{svc: a.Devices},
		ids.System{}.New, cfg.DeviceControl.LeaseDuration, time.Now)
	var ctlHandlers *adapterhttp.DeviceControlHandlers
	if cfg.DeviceControl.Enabled {
		ctlHandlers = &adapterhttp.DeviceControlHandlers{Svc: a.DeviceControl, Log: log}
	}
	dashDevices := &adapterhttp.DashboardDeviceHandlers{
		Svc: a.DeviceControl, Auth: a.Devices, OnlineWindow: cfg.DeviceControl.OnlineWindow,
	}
	a.Handler = adapterhttp.Router(log, a.Health, a.Version, a.Devices, a.Sync, a.notifyProjectors,
		adapterhttp.NewReportHandlers(a.Reports, log), cfg.ReportingToken,
		dashAuth, dashData, dashOrders, commerceWebhooks, notificationWebhooks, ctlHandlers, dashDevices, cfg.DashboardAssetsDir)
	if err := a.VerifySchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return a, nil
}

// commerceOwner names this process for webhook lease observability.
func commerceOwner() string {
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return "cloud-" + hostname
	}
	return "cloud-serve"
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

// ValidateCatalogCategoryPayload is the ingestion-time
// catalog.category.snapshot.v1 gate: event-local validation only, before
// durable ACK. Dependency resolution (missing parents) and DAG defense
// belong to projection, never ingestion.
func ValidateCatalogCategoryPayload(raw json.RawMessage) error {
	p, err := catalog.DecodeCategorySnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateCategorySnapshot(p)
	return err
}

// ValidateCatalogTagPayload is the ingestion-time
// catalog.tag.snapshot.v1 gate: event-local validation only.
func ValidateCatalogTagPayload(raw json.RawMessage) error {
	p, err := catalog.DecodeTagSnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateTagSnapshot(p)
	return err
}

// ValidateCatalogProductPayload is the ingestion-time
// catalog.product.snapshot.v1 gate: event-local validation only.
// Referenced categories/tags may not have projected yet; that wait
// belongs to projection, never ingestion.
func ValidateCatalogProductPayload(raw json.RawMessage) error {
	p, err := catalog.DecodeProductSnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductSnapshot(p)
	return err
}

// ValidateCatalogProductInventoryPayload is the ingestion-time
// inventory.product.snapshot.v1 gate: event-local validation only. A
// missing core product is a projection wait, never an ingestion rejection.
func ValidateCatalogProductInventoryPayload(raw json.RawMessage) error {
	p, err := catalog.DecodeProductInventorySnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductInventorySnapshot(p)
	return err
}

// ValidateCatalogProductSalesPolicyPayload is the ingestion-time
// catalog.product.sales_policy.snapshot.v1 gate: event-local validation
// only. A missing core product is a projection wait, never an ingestion
// rejection.
func ValidateCatalogProductSalesPolicyPayload(raw json.RawMessage) error {
	p, err := catalog.DecodeProductSalesPolicySnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductSalesPolicySnapshot(p)
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
	a.CategoryProjector.Notify()
	a.TagProjector.Notify()
	a.ProductProjector.Notify()
	a.PolicyProjector.Notify()
	a.InventoryProjector.Notify()
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
