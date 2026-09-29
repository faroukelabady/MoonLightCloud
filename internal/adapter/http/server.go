package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
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
func Router(log *slog.Logger, health Health, version Version, devices auth.Service, syncSvc sync.Service, onSyncIngest func(), reports ReportHandlers, reportingToken string, dashAuth DashboardHandlers, dashData DashboardDataHandlers, dashOrders DashboardOrderHandlers, commerceWebhooks *CommerceWebhookHandlers, notificationWebhooks *WhatsAppWebhookHandlers, ctl *DeviceControlHandlers, dashDevices *DashboardDeviceHandlers, ops *OperationsHandlers, assetsDir string) http.Handler {
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
	// Dashboard operator BFF (session cookie; never the reporting token).
	mux.HandleFunc("POST /api/v1/dashboard/auth/login", dashAuth.Login)
	mux.Handle("POST /api/v1/dashboard/auth/logout",
		dashAuth.RequireDashboardSession(RequireSameOrigin(http.HandlerFunc(dashAuth.Logout))))
	mux.Handle("GET /api/v1/dashboard/auth/me",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashAuth.Me)))
	mux.Handle("GET /api/v1/dashboard/overview",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.Overview)))
	mux.Handle("GET /api/v1/dashboard/daily",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.Daily)))
	mux.Handle("GET /api/v1/dashboard/products",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.Products)))
	mux.Handle("GET /api/v1/dashboard/categories",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.Categories)))
	mux.Handle("GET /api/v1/dashboard/branches",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.Branches)))
	mux.Handle("GET /api/v1/dashboard/sync-health",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.SyncHealth)))
	mux.Handle("GET /api/v1/dashboard/activity",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.Activity)))
	mux.Handle("GET /api/v1/dashboard/sales/latest",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashData.LatestSales)))
	mux.Handle("GET /api/v1/dashboard/orders",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashOrders.Orders)))
	mux.Handle("GET /api/v1/dashboard/orders/{provider_key}/{external_order_id}",
		dashAuth.RequireDashboardSession(http.HandlerFunc(dashOrders.OrderDetail)))
	if dashDevices != nil {
		mux.Handle("GET /api/v1/dashboard/devices",
			dashAuth.RequireDashboardSession(http.HandlerFunc(dashDevices.Devices)))
		mux.Handle("POST /api/v1/dashboard/devices/{device_id}/sync-requests",
			dashAuth.RequireDashboardSession(http.HandlerFunc(dashDevices.CreateSyncRequest)))
	}
	// Phase 7D operations incidents (dashboard session only; device
	// credentials never valid here).
	if ops != nil {
		mux.Handle("GET /api/v1/dashboard/operations/incidents",
			dashAuth.RequireDashboardSession(http.HandlerFunc(ops.Incidents)))
		mux.Handle("GET /api/v1/dashboard/operations/incidents/{id}",
			dashAuth.RequireDashboardSession(http.HandlerFunc(ops.IncidentDetail)))
		mux.Handle("POST /api/v1/dashboard/operations/incidents/{id}/acknowledge",
			dashAuth.RequireDashboardSession(http.HandlerFunc(ops.Acknowledge)))
		mux.Handle("POST /api/v1/dashboard/operations/incidents/{id}/resolve",
			dashAuth.RequireDashboardSession(http.HandlerFunc(ops.Resolve)))
		mux.Handle("GET /api/v1/dashboard/operations/summary",
			dashAuth.RequireDashboardSession(http.HandlerFunc(ops.Summary)))
		mux.Handle("GET /api/v1/dashboard/operations/devices/summary",
			dashAuth.RequireDashboardSession(http.HandlerFunc(ops.DeviceSummary)))
	}
	// Provider webhook ingestion is public-but-signed: HMAC authority
	// only, never dashboard session or device tokens.
	if commerceWebhooks != nil {
		mux.Handle("POST /api/v1/commerce/webhooks/woocommerce/{provider_key}",
			http.HandlerFunc(commerceWebhooks.WooCommerceWebhook))
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
