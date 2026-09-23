package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// DashboardHandlers serves the operator BFF under /api/v1/dashboard.
// Session cookie only: the Phase 3A REPORTING_API_TOKEN never appears here.
type DashboardHandlers struct {
	auth       dashboard.Credentials
	sessionKey []byte
	ttl        time.Duration
	secure     bool
	limiter    *dashboard.LoginLimiter
	log        *slog.Logger
}

// NewDashboardHandlers wires BFF handlers. sessionKey must be 32 bytes.
func NewDashboardHandlers(auth dashboard.Credentials, sessionKey []byte, ttl time.Duration, secure bool, log *slog.Logger) DashboardHandlers {
	return DashboardHandlers{auth: auth, sessionKey: sessionKey, ttl: ttl,
		secure: secure, limiter: dashboard.NewLoginLimiter(), log: log}
}

type ctxDashboardKey string

const dashboardUserKey ctxDashboardKey = "dashboard_user"

// RequireDashboardSession guards BFF routes with the session cookie.
// Failures are generic 401s (no distinction between missing/tampered/expired).
func (h DashboardHandlers) RequireDashboardSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(dashboard.SessionCookie)
		if err != nil {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "dashboard session required"))
			return
		}
		user, err := dashboard.VerifySession(h.sessionKey, c.Value, time.Now())
		if err != nil {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "dashboard session required"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), dashboardUserKey, user)))
	})
}

// RequireSameOrigin protects state-changing POSTs (logout): the request
// must come from our own origin. SameSite=Lax already blocks cross-site
// cookie sends on POST; this is defense in depth.
func RequireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = r.Header.Get("Referer")
			}
			if origin != "" && !sameOrigin(origin, r) {
				WriteError(w, r, apperr.New(apperr.Forbidden, "cross-origin request refused"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(origin string, r *http.Request) bool {
	// Compare scheme://host (no path/query). Missing scheme defaults to the
	// request scheme; ports must match exactly.
	lower := strings.ToLower(origin)
	if i := strings.Index(lower, "://"); i >= 0 {
		lower = lower[i+3:]
	}
	if i := strings.IndexByte(lower, '/'); i >= 0 {
		lower = lower[:i]
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := strings.ToLower(r.Host)
	// Accept "scheme://host" or bare "host" forms.
	return lower == host || lower == scheme+"://"+host
}

// clientIP extracts the direct peer IP (no proxy trust: no X-Forwarded-For
// parsing, since deployments vary and spoofing would poison rate limits).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login verifies the operator credential with generic failures and a
// per-IP rate limit. Neither username nor password is ever logged.
func (h DashboardHandlers) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	now := time.Now()
	if h.limiter.Blocked(ip, now) {
		w.Header().Set("Retry-After", "60")
		WriteError(w, r, apperr.New(apperr.TooManyRequests, "too many login attempts"))
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		h.limiter.RecordFailure(ip, now)
		WriteError(w, r, apperr.New(apperr.Unauthorized, "invalid operator credentials"))
		return
	}
	if req.Username == "" || req.Password == "" ||
		req.Username != h.auth.Username ||
		!dashboard.VerifyPassword(req.Password, h.auth.PasswordHash) {
		h.limiter.RecordFailure(ip, now)
		h.log.Info("dashboard login failed", "request_id", RequestID(r))
		WriteError(w, r, apperr.New(apperr.Unauthorized, "invalid operator credentials"))
		return
	}
	h.limiter.RecordSuccess(ip)
	token, err := dashboard.IssueSession(h.sessionKey, h.auth.Username, now.Add(h.ttl))
	if err != nil {
		WriteError(w, r, apperr.New(apperr.Internal, "internal error"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     dashboard.SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(h.ttl.Seconds()),
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
	h.log.Info("dashboard login", "request_id", RequestID(r))
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": h.auth.Username})
}

// Logout clears the session cookie (stateless sessions end client-side).
func (h DashboardHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     dashboard.SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

// Me reports session state for the frontend shell.
func (h DashboardHandlers) Me(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(dashboardUserKey).(string)
	if user == "" {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "dashboard session required"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": user})
}
