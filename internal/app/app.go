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
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	shopifyadapter "github.com/faroukelabady/MoonLightCloud/internal/commerce/shopify"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/woocommerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/telegram"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/whatsapp"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
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
	Cfg                           config.Config
	Log                           *slog.Logger
	Pool                          *pgxpool.Pool
	Devices                       auth.Service
	Sync                          sync.Service
	Reports                       report.Service
	Dashboard                     dashboard.Service
	Projector                     *sale.Projector
	SaleStore                     sale.Store
	ReturnProjector               *returnrefund.Projector
	ReturnStore                   returnrefund.Store
	CatalogStore                  catalog.Store
	CategoryProjector             *catalog.Projector
	TagProjector                  *catalog.Projector
	ProductProjector              *catalog.Projector
	PolicyProjector               *catalog.Projector
	InventoryProjector            *catalog.Projector
	ProductConfigurationProjector *catalog.Projector
	ProductVariantProjector       *catalog.Projector
	VariantInventoryProjector     *catalog.Projector
	ProductTypeProjector          *catalog.Projector
	Catalog                       catalog.Service
	CommerceRegistry              *commerce.Registry
	CommerceService               *commerce.CommerceService
	ReevaluationWorker            *commerce.ReevaluationWorker
	OrderService                  *orders.OrderService
	OrderProcessor                *orders.Processor
	NotificationRegistry          *notifications.Registry
	NotificationDispatcher        *notifications.Dispatcher
	ReportService                 *businessreports.Service
	ReportPlanner                 *businessreports.Planner
	ReportRunner                  *businessreports.Runner
	DeviceControl                 *devicecontrol.Service
	OperationsService             *operations.Service
	OperationsReader              *operations.OpsReader
	OperationsEngine              *operations.Engine
	Handler                       http.Handler
	Health                        adapterhttp.Health
	Version                       adapterhttp.Version
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
	sync.RegisterEventType(sale.EventSaleFinalizedV2, ValidateSaleV2Payload)
	sync.RegisterEventType(sale.EventSaleFinalizedV3, ValidateSaleV3Payload)
	sync.RegisterEventType(returnrefund.EventReturnRefundFinalizedV1, ValidateReturnRefundPayload)
	sync.RegisterEventType(catalog.EventCategorySnapshotV1, ValidateCatalogCategoryPayload)
	sync.RegisterEventType(catalog.EventCategorySnapshotV2, ValidateCatalogCategoryV2Payload)
	sync.RegisterEventType(catalog.EventTagSnapshotV1, ValidateCatalogTagPayload)
	sync.RegisterEventType(catalog.EventProductSnapshotV1, ValidateCatalogProductPayload)
	sync.RegisterEventType(catalog.EventProductSnapshotV2, ValidateCatalogProductV2Payload)
	sync.RegisterEventType(catalog.EventProductSalesPolicySnapshotV1, ValidateCatalogProductSalesPolicyPayload)
	sync.RegisterEventType(catalog.EventInventoryProductSnapshotV1, ValidateCatalogProductInventoryPayload)
	sync.RegisterEventType(catalog.EventProductConfigurationSnapshotV1, ValidateCatalogProductConfigurationsPayload)
	sync.RegisterEventType(catalog.EventProductVariantSnapshotV1, ValidateCatalogProductVariantPayload)
	sync.RegisterEventType(catalog.EventProductTypeSnapshotV1, ValidateCatalogProductTypePayload)
	sync.RegisterEventType(catalog.EventInventoryProductVariantSnapshotV1, ValidateCatalogProductVariantInventoryPayload)
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
	a.ProductConfigurationProjector = catalog.NewProductConfigurationsProjector(store, clock.System{}, log)
	a.ProductVariantProjector = catalog.NewProductVariantProjector(store, clock.System{}, log)
	a.ProductTypeProjector = catalog.NewProductTypeProjector(store, clock.System{}, log)
	a.VariantInventoryProjector = catalog.NewProductVariantInventoryProjector(store, clock.System{}, log)
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
	var shopifyWebhooks *adapterhttp.ShopifyWebhookHandlers
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
	}
	if cfg.Shopify.Enabled {
		provider, err := shopifyadapter.NewShopifyProvider(cfg.Shopify, nil, store)
		if err != nil {
			pool.Close()
			return nil, err
		}
		if err := a.CommerceRegistry.Register(provider.Key(), provider); err != nil {
			pool.Close()
			return nil, err
		}
	}
	ordersEnabled := (cfg.WooCommerce.Enabled && cfg.WooCommerce.OrdersEnabled) ||
		(cfg.Shopify.Enabled && cfg.Shopify.OrdersEnabled)
	if a.CommerceRegistry.Count() > 0 {
		a.OrderService = orders.NewOrderService(a.CommerceRegistry, store, log)
	}
	if ordersEnabled {
		a.OrderProcessor = orders.NewProcessor(store, a.OrderService, orders.SystemClock{}, commerceOwner(), log)
		if cfg.WooCommerce.Enabled && cfg.WooCommerce.OrdersEnabled {
			secret := cfg.WooCommerce.WebhookSecret
			key := cfg.WooCommerce.ProviderKey
			commerceWebhooks = adapterhttp.NewCommerceWebhookHandlers(store,
				func(providerKey string) (string, bool) {
					if providerKey == key {
						return secret, true
					}
					return "", false
				}, a.OrderProcessor.Notify, log)
		}
		if cfg.Shopify.Enabled && cfg.Shopify.OrdersEnabled {
			secret := cfg.Shopify.ClientSecret
			domain := cfg.Shopify.NormalizedShopDomain()
			key := cfg.Shopify.ProviderKey
			shopifyWebhooks = adapterhttp.NewShopifyWebhookHandlers(store,
				func(providerKey string) (adapterhttp.ShopifyWebhookConfig, bool) {
					if providerKey == key {
						return adapterhttp.ShopifyWebhookConfig{ClientSecret: secret, ShopDomain: domain}, true
					}
					return adapterhttp.ShopifyWebhookConfig{}, false
				}, a.OrderProcessor.Notify, log)
		}
	}
	if a.CommerceRegistry.Count() > 0 {
		// Phase 13 §83: generic commerce orchestration + the durable
		// re-evaluation worker. Interval scan only (no in-process
		// queues): enqueue happens atomically inside catalog/product
		// projections and the worker converges affected Products through
		// the exact same CommerceService the manual CLI uses. The store
		// doubles as the durable queue; no provider I/O runs in any
		// projection transaction.
		a.CommerceService = commerce.NewCommerceService(a.CommerceRegistry, store,
			commerce.NewCatalogCommerceSource(catalog.NewService(store)), log)
		a.ReevaluationWorker = commerce.NewReevaluationWorker(store, a.CommerceService, a.CommerceRegistry, time.Now, log)
	}
	a.Log.Info("commerce providers", "count", a.CommerceRegistry.Count(),
		"orders", ordersEnabled)
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
	// Phase 10 second provider: Telegram registers on the same generic
	// registry under its own logical key. No startup API request: the
	// first network call happens at dispatch, so Cloud starts even if
	// Telegram is temporarily unreachable.
	if cfg.TelegramNotifications.Enabled {
		provider, err := telegram.NewProvider(cfg.TelegramNotifications, nil)
		if err != nil {
			pool.Close()
			return nil, err
		}
		// A duplicate logical key across providers fails startup
		// safely: no partial registration, no silent shadowing.
		if err := a.NotificationRegistry.Register(provider.Key(), provider); err != nil {
			pool.Close()
			return nil, err
		}
	}
	// The canonical dispatcher serves every registered provider: one
	// worker, no Telegram-specific path. It runs whenever at least one
	// provider is configured; with zero providers there is nothing to
	// lease and the dispatcher stays nil.
	if cfg.WhatsAppNotifications.Enabled || cfg.TelegramNotifications.Enabled {
		a.NotificationDispatcher = notifications.NewDispatcher(
			store, a.NotificationRegistry, notifications.SystemClock{}, commerceOwner(), log)
	}
	a.Log.Info("notification providers", "count", a.NotificationRegistry.Count(),
		"whatsapp", cfg.WhatsAppNotifications.Enabled,
		"telegram", cfg.TelegramNotifications.Enabled)
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
	opsStore := store
	opsService := operations.NewService(opsStore, ids.System{}.New, time.Now)
	opsMetrics := operations.NewMetrics()
	opsReader := operations.NewOpsReader(opsStore, opsService, opsMetrics)
	opsHandlers := &adapterhttp.OperationsHandlers{Svc: opsService, Ops: opsReader}
	a.OperationsService = opsService
	a.OperationsReader = opsReader
	a.Log.Info("operations", "enabled", cfg.Operations.Enabled,
		"auto_sync_on_reconnect", cfg.Operations.AutoSyncOnReconnect)
	a.DeviceControl = devicecontrol.NewService(store, authDeviceStatus{svc: a.Devices},
		ids.System{}.New, cfg.DeviceControl.LeaseDuration, time.Now)
	var ctlHandlers *adapterhttp.DeviceControlHandlers
	if cfg.DeviceControl.Enabled {
		ctlHandlers = &adapterhttp.DeviceControlHandlers{Svc: a.DeviceControl, Log: log}
	}
	dashDevices := &adapterhttp.DashboardDeviceHandlers{
		Svc: a.DeviceControl, Auth: a.Devices, Stores: store, OnlineWindow: cfg.DeviceControl.OnlineWindow,
	}
	// Phase 16 catalog admin: durable operator intent + Retail pull.
	// Always wired (routes registered unconditionally): authorization
	// is per-request via dashboard session / device credential, and
	// commands only flow to capable bound devices.
	catalogAdminSvc := catalogadmin.NewService(store, catalogAdminDevices{stores: store, auth: a.Devices})
	catalogAdminHandlers := &adapterhttp.CatalogAdminHandlers{Svc: catalogAdminSvc, Log: log}
	opsNotify := notifications.NewService(opsStore, opsStore, log)
	opsAlerts := operations.NewAlertProcessor(opsStore, opsService, opsNotify, ids.System{}.New, time.Now, opsMetrics)
	opsDetector := operations.NewDetector(opsStore, opsService, opsAlerts, operations.DetectorConfig{
		BatchSize: cfg.Operations.ScanBatchSize, OfflineAfter: cfg.Operations.DeviceOfflineAfter,
		OnlineWindow: cfg.DeviceControl.OnlineWindow, SyncPendingStale: cfg.Operations.SyncPendingStaleAfter,
		SyncRunningStale: cfg.Operations.SyncRunningStaleAfter, ReportStale: cfg.Operations.ReportStaleAfter,
		NotificationStale:   cfg.Operations.NotificationRetryStaleAfter,
		AutoSyncOnReconnect: cfg.Operations.AutoSyncOnReconnect,
	}, ids.System{}.New, time.Now, opsMetrics)
	opsReader.SetManualGuard(cfg.Operations.DeviceOfflineAfter)
	opsRecovery := operations.NewRecoveryWorker(opsStore, operations.NewDeviceCommander(a.DeviceControl), time.Now, opsMetrics)
	// The detector, alert processor, and recovery worker are always
	// constructed so dashboard predicate checks (manual resolve guard)
	// work even with the engine disabled; only the background loops are
	// gated on OPERATIONS_ENABLED.
	if cfg.Operations.Enabled {
		a.OperationsEngine = operations.NewEngine(opsDetector, opsAlerts, opsRecovery,
			cfg.Operations.ScanInterval, cfg.Operations.ScanBatchSize, log)
	}
	a.Handler = adapterhttp.Router(log, a.Health, a.Version, a.Devices, a.Sync, a.notifyProjectors,
		adapterhttp.NewReportHandlers(a.Reports, log), cfg.ReportingToken,
		dashAuth, dashData, dashOrders, commerceWebhooks, shopifyWebhooks, notificationWebhooks, ctlHandlers, dashDevices, opsHandlers, store, catalogAdminHandlers, cfg.DashboardAssetsDir)
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

// ValidateSaleV2Payload is the ingestion-time sale.finalized.v2 gate.
// v1 events keep flowing through ValidateSalePayload with unknown tag
// capture; v2 additionally freezes historical tag snapshots.
func ValidateSaleV2Payload(raw json.RawMessage) error {
	p, err := sale.DecodeV2(raw)
	if err != nil {
		return err
	}
	_, err = sale.ValidateV2(p)
	return err
}

// ValidateSaleV3Payload is the ingestion-time sale.finalized.v3 gate
// (Phase 17-R0): every v2 invariant plus the frozen per-line variant
// snapshots (variant SKU, option attribute labels, sale-time pricing
// override). v1/v2 keep their own gates and stay fully supported.
func ValidateSaleV3Payload(raw json.RawMessage) error {
	p, err := sale.DecodeV3(raw)
	if err != nil {
		return err
	}
	_, err = sale.ValidateV3(p)
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

// ValidateCatalogCategoryV2Payload is the ingestion-time validator for
// catalog.category.snapshot.v2 (Phase 13): the v1 invariants plus the
// required ONLINE channel policy field.
func ValidateCatalogCategoryV2Payload(raw json.RawMessage) error {
	decoded, err := catalog.DecodeCategorySnapshotV2(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateCategorySnapshot(decoded)
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

// ValidateCatalogProductV2Payload is the ingestion-time
// catalog.product.snapshot.v2 gate (Phase 17): the v1 invariants with the
// deprecated primary_variant_sku display mirror in place of sku.
func ValidateCatalogProductV2Payload(raw json.RawMessage) error {
	p, err := catalog.DecodeProductSnapshotV2(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductSnapshotV2(p)
	return err
}

// ValidateCatalogProductVariantPayload is the ingestion-time
// catalog.product_variant.snapshot.v1 gate (Phase 17): event-local
// validation only. A missing core product is a projection wait, never an
// ingestion rejection.
// ValidateCatalogProductTypePayload is the ingestion-time
// catalog.product_type.snapshot.v1 gate (Phase 17-R2): event-local
// validation only. A missing type is a projection wait, never an
// ingestion rejection.
func ValidateCatalogProductTypePayload(raw json.RawMessage) error {
	p, err := catalog.DecodeProductTypeSnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductTypeSnapshot(p)
	return err
}

func ValidateCatalogProductVariantPayload(raw json.RawMessage) error {
	p, err := catalog.DecodeProductVariantSnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductVariantSnapshot(p)
	return err
}

// ValidateCatalogProductVariantInventoryPayload is the ingestion-time
// inventory.product_variant.snapshot.v1 gate (Phase 17): event-local
// validation only. A missing variant is a projection wait, never an
// ingestion rejection.
func ValidateCatalogProductVariantInventoryPayload(raw json.RawMessage) error {
	p, err := catalog.DecodeProductVariantInventorySnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductVariantInventorySnapshot(p)
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
	if a.ProductConfigurationProjector != nil {
		a.ProductConfigurationProjector.Notify()
	}
	if a.ProductVariantProjector != nil {
		a.ProductVariantProjector.Notify()
	}
	if a.VariantInventoryProjector != nil {
		a.VariantInventoryProjector.Notify()
	}
	if a.ProductTypeProjector != nil {
		a.ProductTypeProjector.Notify()
	}
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

// ValidateCatalogProductConfigurationsPayload is the ingestion-time gate
// for catalog.product.configuration.snapshot.v1 (Phase 15): event-local
// validation only, before durable ACK.
func ValidateCatalogProductConfigurationsPayload(raw json.RawMessage) error {
	decoded, err := catalog.DecodeProductConfigurationsSnapshot(raw)
	if err != nil {
		return err
	}
	_, err = catalog.ValidateProductConfigurationsSnapshot(decoded)
	return err
}
