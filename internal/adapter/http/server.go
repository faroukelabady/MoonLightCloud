package http

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
	"github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// Config tunes server safety defaults.
type Config struct {
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	ReadHeaderTimeout time.Duration
}

const (
	defaultMaxHeaderBytes = 1 << 20 // 1 MiB
	// 8 MiB global ceiling; the sync endpoint enforces its own tighter
	// envelope limits (max events, max payload) inside this bound.
	defaultMaxBodyBytes = 8 << 20
)

// Router builds the mux with middleware. CORS stays disabled: no browser
// client exists yet. Future rate limiting belongs here as middleware.
func Router(log *slog.Logger, health Health, version Version, devices auth.Service, syncSvc sync.Service, onSyncIngest func(), reports ReportHandlers, reportingToken string, humanAuth *HumanAuth, dashData DashboardDataHandlers, dashOrders DashboardOrderHandlers, commerceWebhooks *CommerceWebhookHandlers, shopifyWebhooks *ShopifyWebhookHandlers, notificationWebhooks *WhatsAppWebhookHandlers, ctl *DeviceControlHandlers, dashDevices *DashboardDeviceHandlers, ops *OperationsHandlers, storeReg StoreRegistrar, catalogAdmin *CatalogAdminHandlers, updates *UpdateHandlers, assetsDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", health.ServeLive)
	mux.HandleFunc("GET /health/ready", health.ServeReady)
	mux.HandleFunc("GET /version", version.Handler)
	mux.Handle("GET /api/v1/device/ping",
		DeviceAuth(devices)(http.HandlerFunc(DevicePing)))
	mux.Handle("GET /api/v1/sync/capabilities",
		DeviceAuth(devices)(SyncCapabilities(syncSvc)))
	mux.Handle("POST /api/v1/sync/batches",
		DeviceAuth(devices)(SyncBatch(syncSvc, onSyncIngest)))
	mux.Handle("POST /api/v1/sync/store-registration",
		DeviceAuth(devices)(StoreRegistration(storeReg)))
	// Phase 7C device control plane (Retail-initiated polling only).
	// Registered only when the control plane is enabled; otherwise 404.
	if ctl != nil {
		mux.Handle("POST /api/v1/device-control/poll",
			DeviceAuth(devices)(http.HandlerFunc(ctl.Poll)))
		mux.Handle("POST /api/v1/device-control/commands/{id}/accepted",
			DeviceAuth(devices)(http.HandlerFunc(ctl.Accepted)))
		mux.Handle("POST /api/v1/device-control/commands/{id}/status",
			DeviceAuth(devices)(http.HandlerFunc(ctl.Status)))
	}
	mux.Handle("GET /api/v1/reports/sales/summary",
		ReportAuth(reportingToken)(http.HandlerFunc(reports.SalesSummary)))
	mux.Handle("GET /api/v1/reports/sales/daily",
		ReportAuth(reportingToken)(http.HandlerFunc(reports.SalesDaily)))
	mux.Handle("GET /api/v1/reports/sales/breakdown",
		ReportAuth(reportingToken)(http.HandlerFunc(reports.SalesBreakdown)))
	// Dashboard (ADR-0053): human authentication + named permission +
	// server-side Store scope on EVERY route; CSRF (same-origin + per-session
	// token) on every mutation. Device credentials are never valid here.
	humanAuth.AuthRoutes(mux)
	g := humanAuth.Guard
	mux.Handle("GET /api/v1/dashboard/overview", g(humanauth.PermReportsRead, ScopeQuery, dashData.Overview))
	mux.Handle("GET /api/v1/dashboard/daily", g(humanauth.PermReportsRead, ScopeQuery, dashData.Daily))
	mux.Handle("GET /api/v1/dashboard/tags", g(humanauth.PermReportsRead, ScopeQuery, dashData.Tags))
	mux.Handle("GET /api/v1/dashboard/orders/summary", g(humanauth.PermReportsRead, ScopeQuery, dashData.OrderAnalytics))
	mux.Handle("GET /api/v1/dashboard/catalog-health", g(humanauth.PermCatalogRead, ScopeQuery, dashData.CatalogHealth))
	mux.Handle("GET /api/v1/dashboard/products", g(humanauth.PermReportsRead, ScopeQuery, dashData.Products))
	mux.Handle("GET /api/v1/dashboard/categories", g(humanauth.PermReportsRead, ScopeQuery, dashData.Categories))
	mux.Handle("GET /api/v1/dashboard/branches", g(humanauth.PermReportsRead, ScopeQuery, dashData.Branches))
	mux.Handle("GET /api/v1/dashboard/sync-health", g(humanauth.PermReportsRead, ScopeQuery, dashData.SyncHealth))
	mux.Handle("GET /api/v1/dashboard/activity", g(humanauth.PermReportsRead, ScopeQuery, dashData.Activity))
	mux.Handle("GET /api/v1/dashboard/sales/latest", g(humanauth.PermReportsRead, ScopeQuery, dashData.LatestSales))
	mux.Handle("GET /api/v1/dashboard/orders", g(humanauth.PermReportsRead, ScopeQuery, dashOrders.Orders))
	mux.Handle("GET /api/v1/dashboard/orders/{provider_key}/{external_order_id}", g(humanauth.PermReportsRead, ScopeQuery, dashOrders.OrderDetail))
	if dashDevices != nil {
		mux.Handle("GET /api/v1/dashboard/devices", g(humanauth.PermDevicesRead, ScopeHandler, dashDevices.Devices))
		if dashDevices.Stores != nil {
			mux.Handle("GET /api/v1/dashboard/stores", g(humanauth.PermDevicesRead, ScopeHandler, (&StoreHandlers{Stores: dashDevices.Stores}).ListStores))
		}
		mux.Handle("POST /api/v1/dashboard/devices/{device_id}/sync-requests", g(humanauth.PermDevicesManage, ScopeHandler, dashDevices.CreateSyncRequest))
	}
	// Phase 16 catalog admin: Retail pulls targets over its outbound
	// device channel; operators act through the guarded dashboard.
	// Registered only when the admin service is wired; otherwise 404.
	if catalogAdmin != nil {
		mux.Handle("GET /api/v1/device-control/catalog-commands",
			DeviceAuth(devices)(http.HandlerFunc(catalogAdmin.PollCatalogCommands)))
		mux.Handle("POST /api/v1/device-control/catalog-commands/{target_id}/result",
			DeviceAuth(devices)(http.HandlerFunc(catalogAdmin.ReportCatalogResult)))
		mux.Handle("POST /api/v1/device-control/capabilities",
			DeviceAuth(devices)(http.HandlerFunc(catalogAdmin.ReportCapabilities)))
		mux.Handle("POST /api/v1/dashboard/catalog-admin/commands", g(humanauth.PermCatalogManage, ScopeHandler, catalogAdmin.CreateCommand))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/commands", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.ListCommands))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/commands/{id}", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.GetCommand))
		mux.Handle("POST /api/v1/dashboard/catalog-admin/commands/{id}/cancel", g(humanauth.PermCatalogManage, ScopeQuery, catalogAdmin.CancelCommand))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/products", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminProducts))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/products/{id}", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminProductDetail))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/products/{id}/configurations", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminConfigurations))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/products/{id}/variants", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminProductVariants))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/product-types", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminProductTypes))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/product-types/{id}", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminProductTypeDetail))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/categories", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminCategories))
		mux.Handle("GET /api/v1/dashboard/catalog-admin/tags", g(humanauth.PermCatalogRead, ScopeQuery, catalogAdmin.AdminTags))
	}
	// Phase 18 release registry, fleet rollout control and device update
	// channel (ADR-0051/0052). Releases are global infrastructure: reading
	// needs releases.read, importing/revoking (affects every Store) needs
	// releases.manage on an all-Stores account. Device routes take identity
	// from the device credential only.
	if updates != nil {
		mux.Handle("GET /api/v1/dashboard/releases", g(humanauth.PermReleasesRead, ScopeNone, updates.ListReleases))
		mux.Handle("POST /api/v1/dashboard/releases", g(humanauth.PermReleasesManage, ScopeAllStores, updates.ImportRelease))
		mux.Handle("GET /api/v1/dashboard/releases/{id}", g(humanauth.PermReleasesRead, ScopeNone, updates.GetRelease))
		mux.Handle("POST /api/v1/dashboard/releases/{id}/status", g(humanauth.PermReleasesManage, ScopeAllStores, updates.SetReleaseStatus))
		mux.Handle("GET /api/v1/dashboard/rollouts", g(humanauth.PermRolloutsRead, ScopeQuery, updates.ListRollouts))
		mux.Handle("POST /api/v1/dashboard/rollouts", g(humanauth.PermRolloutsManage, ScopeHandler, updates.CreateRollout))
		mux.Handle("GET /api/v1/dashboard/rollouts/{id}", g(humanauth.PermRolloutsRead, ScopeHandler, updates.GetRollout))
		mux.Handle("GET /api/v1/dashboard/rollouts/{id}/targets", g(humanauth.PermRolloutsRead, ScopeQuery, updates.ListTargets))
		mux.Handle("POST /api/v1/dashboard/rollouts/{id}/{action}", g(humanauth.PermRolloutsManage, ScopeHandler, updates.RolloutAction))
		mux.Handle("GET /api/v1/dashboard/update-targets/{id}/history", g(humanauth.PermRolloutsRead, ScopeAllStores, updates.TargetHistory))
		mux.Handle("GET /api/v1/dashboard/fleet", g(humanauth.PermDevicesRead, ScopeQuery, updates.Fleet))
		mux.Handle("GET /api/v1/dashboard/update-audit", g(humanauth.PermRolloutsRead, ScopeQuery, updates.Audit))
		mux.Handle("POST /api/v1/device-control/update/status",
			DeviceAuth(devices)(http.HandlerFunc(updates.DeviceStatus)))
		mux.Handle("GET /api/v1/device-control/update/command",
			DeviceAuth(devices)(http.HandlerFunc(updates.DeviceCommand)))
		mux.Handle("POST /api/v1/device-control/update/targets/{id}/report",
			DeviceAuth(devices)(http.HandlerFunc(updates.DeviceReport)))
	}
	// Phase 7D operations incidents span every Store: all-Stores humans
	// only (device credentials never valid here).
	if ops != nil {
		mux.Handle("GET /api/v1/dashboard/operations/incidents", g(humanauth.PermOperationsRead, ScopeAllStores, ops.Incidents))
		mux.Handle("GET /api/v1/dashboard/operations/incidents/{id}", g(humanauth.PermOperationsRead, ScopeAllStores, ops.IncidentDetail))
		mux.Handle("POST /api/v1/dashboard/operations/incidents/{id}/acknowledge", g(humanauth.PermOperationsAct, ScopeAllStores, ops.Acknowledge))
		mux.Handle("POST /api/v1/dashboard/operations/incidents/{id}/resolve", g(humanauth.PermOperationsAct, ScopeAllStores, ops.Resolve))
		mux.Handle("GET /api/v1/dashboard/operations/summary", g(humanauth.PermOperationsRead, ScopeAllStores, ops.Summary))
		mux.Handle("GET /api/v1/dashboard/operations/devices/summary", g(humanauth.PermOperationsRead, ScopeAllStores, ops.DeviceSummary))
	}
	// Provider webhook ingestion is public-but-signed: HMAC authority
	// only, never dashboard session or device tokens. Both commerce
	// providers share the generic inbox and reconciliation pipeline.
	if commerceWebhooks != nil {
		mux.Handle("POST /api/v1/commerce/webhooks/woocommerce/{provider_key}",
			http.HandlerFunc(commerceWebhooks.WooCommerceWebhook))
	}
	if shopifyWebhooks != nil {
		mux.Handle("POST /api/v1/commerce/webhooks/shopify/{provider_key}",
			http.HandlerFunc(shopifyWebhooks.ShopifyWebhook))
	}
	// WhatsApp notification callbacks are public-but-signed: verify-token
	// (GET) or app-secret HMAC (POST) authority only, never dashboard
	// session or device tokens. Unregistered while disabled.
	if notificationWebhooks != nil {
		mux.Handle("GET /api/v1/notifications/webhooks/whatsapp/{provider_key}",
			http.HandlerFunc(notificationWebhooks.VerifyWhatsAppWebhook))
		mux.Handle("POST /api/v1/notifications/webhooks/whatsapp/{provider_key}",
			http.HandlerFunc(notificationWebhooks.StatusWhatsAppWebhook))
	}
	// Dashboard SPA (static build; API routes above take precedence).
	mux.Handle("/dashboard", DashboardAssets(assetsDir, log))
	mux.Handle("/dashboard/", DashboardAssets(assetsDir, log))
	mux.HandleFunc("/", NotFound)

	var h http.Handler = mux
	h = apiSecurityHeaders(h)
	h = limitBody(defaultMaxBodyBytes)(h)
	h = AccessLog(log, h)
	h = RequestIDMiddleware(h)
	h = Recover(h)
	return h
}

// Server builds the http.Server with baseline timeouts and header limits.
func Server(addr string, handler http.Handler, c Config) *http.Server {
	read := c.ReadTimeout
	if read == 0 {
		read = 10 * time.Second
	}
	write := c.WriteTimeout
	if write == 0 {
		write = 15 * time.Second
	}
	idle := c.IdleTimeout
	if idle == 0 {
		idle = 60 * time.Second
	}
	maxHeader := c.MaxHeaderBytes
	if maxHeader == 0 {
		maxHeader = defaultMaxHeaderBytes
	}
	readHeader := c.ReadHeaderTimeout
	if readHeader == 0 {
		readHeader = 5 * time.Second
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       read,
		WriteTimeout:      write,
		IdleTimeout:       idle,
		ReadHeaderTimeout: readHeader,
		MaxHeaderBytes:    maxHeader,
	}
}

// limitBody rejects unbounded payloads early.
func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if max > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// apiSecurityHeaders (ADR-0053): API responses are never sniffed, and
// dashboard (human) API responses are never cached, so Back navigation
// after logout cannot replay protected data from the browser cache.
func apiSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if strings.HasPrefix(r.URL.Path, "/api/v1/dashboard/") {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Referrer-Policy", "same-origin")
			}
		}
		next.ServeHTTP(w, r)
	})
}
