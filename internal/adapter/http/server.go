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
func Router(log *slog.Logger, health Health, version Version, devices auth.Service, syncSvc sync.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", health.ServeLive)
	mux.HandleFunc("GET /health/ready", health.ServeReady)
	mux.HandleFunc("GET /version", version.Handler)
	mux.Handle("GET /api/v1/device/ping",
		DeviceAuth(devices)(http.HandlerFunc(DevicePing)))
	mux.Handle("GET /api/v1/sync/capabilities",
		DeviceAuth(devices)(SyncCapabilities(syncSvc)))
	mux.Handle("POST /api/v1/sync/batches",
		DeviceAuth(devices)(SyncBatch(syncSvc)))
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
