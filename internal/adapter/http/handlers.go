package http

import (
	"context"
	"net/http"

	"github.com/MoonLightSoftware/MoonLightCloud/internal/apperr"
)

// Health reports liveness/readiness state supplied by the app layer.
type Health struct {
	LiveCheck  func() bool
	ReadyCheck func(ctx context.Context) error
}

// Version carries build metadata. Commit/build time inject via ldflags.
type Version struct {
	App       string `json:"app"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

func statusOK() map[string]string { return map[string]string{"status": "ok"} }

// ServeLive answers 200 while the process serves HTTP.
func (h Health) ServeLive(w http.ResponseWriter, r *http.Request) {
	if h.LiveCheck != nil && !h.LiveCheck() {
		WriteError(w, r, apperr.New(apperr.Unavailable, "not live"))
		return
	}
	writeJSON(w, http.StatusOK, statusOK())
}

// ServeReady answers 200 only when DB connectivity and schema checks pass.
func (h Health) ServeReady(w http.ResponseWriter, r *http.Request) {
	if err := h.ReadyCheck(r.Context()); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, statusOK())
}

// VersionHandler returns build metadata without secrets or paths.
func (v Version) Handler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, v)
}

// DevicePing proves device auth end to end. No secret material in output.
func DevicePing(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":     dev.ID,
		"authenticated": true,
	})
}
