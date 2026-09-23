package http

import (
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// DashboardAssets serves the built Svelte SPA from dir under /dashboard
// with SPA fallback to index.html. Directory listings are disabled;
// dotfiles never served. Security headers apply to every dashboard
// response (no inline scripts required; all assets are local files).
func DashboardAssets(dir string, log *slog.Logger) http.Handler {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
			return
		}
		setDashboardSecurityHeaders(w)
		rel := strings.TrimPrefix(path.Clean(r.URL.Path), "/dashboard")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			serveDashboardFile(w, r, filepath.Join(abs, "index.html"), log)
			return
		}
		if strings.Contains(rel, "..") || strings.HasPrefix(rel, ".") {
			WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
			return
		}
		full := filepath.Join(abs, filepath.FromSlash(rel))
		if !strings.HasPrefix(full, abs) {
			WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
			return
		}
		if st, err := os.Stat(full); err != nil || st.IsDir() {
			// SPA subroutes fall back to the shell.
			serveDashboardFile(w, r, filepath.Join(abs, "index.html"), log)
			return
		}
		serveDashboardFile(w, r, full, log)
	})
}

func serveDashboardFile(w http.ResponseWriter, r *http.Request, full string, log *slog.Logger) {
	if _, err := os.Stat(full); err != nil {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	http.ServeFile(w, r, full)
}

// setDashboardSecurityHeaders locks down dashboard responses: same-origin
// everything, no framing, no MIME sniffing. No 'unsafe-inline' anywhere —
// the Vite build emits separate asset files.
func setDashboardSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'self'; base-uri 'self'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
}
