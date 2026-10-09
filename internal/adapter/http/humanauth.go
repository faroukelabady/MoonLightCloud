package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
)

// HumanAuth serves human authentication, the authorization middleware for
// every dashboard API, and OWNER user administration (ADR-0053).
//
// Browser credential: one opaque HttpOnly, SameSite=Strict, Path=/ session
// cookie (Secure and "__Host-" prefixed in production). State-changing
// requests additionally require a same-origin Origin/Referer AND the
// per-session CSRF token in X-CSRF-Token. Device credentials are never
// accepted here; human sessions are never accepted on device routes.
type HumanAuth struct {
	Svc     *humanauth.Service
	Secure  bool
	Trusted []net.IPNet
	Log     *slog.Logger
}

// CSRFHeader carries the per-session CSRF token on mutations.
const CSRFHeader = "X-CSRF-Token"

func (h *HumanAuth) cookieName() string {
	if h.Secure {
		return "__Host-mlc_session"
	}
	return "mlc_session"
}

func (h *HumanAuth) setCookie(w http.ResponseWriter, s humanauth.IssuedSession) {
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: s.Token, Path: "/", Expires: s.ExpiresAt,
		HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteStrictMode})
}

func (h *HumanAuth) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteStrictMode})
}

func (h *HumanAuth) clientIP(r *http.Request) string {
	return dashboard.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), h.Trusted)
}

type ctxAuthKey struct{}

// AuthOf returns the authenticated human session of a request.
func AuthOf(r *http.Request) (humanauth.Authenticated, bool) {
	a, ok := r.Context().Value(ctxAuthKey{}).(humanauth.Authenticated)
	return a, ok
}

// PrincipalOf returns the authenticated human of a request.
func PrincipalOf(r *http.Request) (humanauth.Principal, bool) {
	a, ok := AuthOf(r)
	return a.Principal, ok
}

// unsafeMethod reports state-changing methods.
func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// requireOrigin demands same-origin evidence on a mutation: Origin (or
// Referer) must be present and match the request host.
func requireOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	return origin != "" && sameOrigin(origin, r)
}

// session authenticates the cookie and enforces CSRF on mutations. stages
// lists the session stages allowed through.
func (h *HumanAuth) session(stages []humanauth.Stage, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(h.cookieName())
		if err != nil {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "AUTH_REQUIRED"))
			return
		}
		a, err := h.Svc.Authenticate(r.Context(), c.Value)
		if err != nil {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "AUTH_REQUIRED"))
			return
		}
		allowed := false
		for _, s := range stages {
			if a.Session.Stage == s {
				allowed = true
			}
		}
		if !allowed {
			code := "MFA_REQUIRED"
			if a.Session.Stage == humanauth.StageMFASetup {
				code = "MFA_SETUP_REQUIRED"
			}
			WriteError(w, r, apperr.New(apperr.Forbidden, code))
			return
		}
		if unsafeMethod(r.Method) {
			if !requireOrigin(r) || !humanauth.CheckCSRF(a.Session, r.Header.Get(CSRFHeader)) {
				WriteError(w, r, apperr.New(apperr.Forbidden, "CSRF_REJECTED"))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxAuthKey{}, a)))
	})
}

// StoreScope selects how a route's Store access is enforced.
type StoreScope int

const (
	// ScopeNone: no Store dimension (global infrastructure reads, own
	// account) or the service enforces it (user administration).
	ScopeNone StoreScope = iota
	// ScopeAllStores: only all-Stores principals (cross-Store data).
	ScopeAllStores
	// ScopeQuery: the store_id query parameter; empty means "every Store"
	// and requires an all-Stores principal.
	ScopeQuery
	// ScopeHandler: the handler resolves the owning Store and calls
	// RequireStore itself.
	ScopeHandler
)

// Guard wraps a dashboard handler with full authentication (FULL stage),
// CSRF on mutations, the named permission and the Store scope. Denials on
// mutation routes are written to the security audit.
func (h *HumanAuth) Guard(perm humanauth.Permission, scope StoreScope, handler http.HandlerFunc) http.Handler {
	return h.session([]humanauth.Stage{humanauth.StageFull}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalOf(r)
		if !p.Can(perm) {
			h.deny(r, p, "PERMISSION", string(perm))
			WriteError(w, r, apperr.New(apperr.Forbidden, "FORBIDDEN"))
			return
		}
		switch scope {
		case ScopeAllStores:
			if !p.AllStores {
				h.deny(r, p, "STORE_SCOPE", "")
				WriteError(w, r, apperr.New(apperr.Forbidden, "STORE_FORBIDDEN"))
				return
			}
		case ScopeQuery:
			storeID := r.URL.Query().Get("store_id")
			if storeID == "" && !p.AllStores {
				WriteError(w, r, apperr.New(apperr.Forbidden, "STORE_SCOPE_REQUIRED"))
				return
			}
			if storeID != "" && !p.CanAccessStore(storeID) {
				h.deny(r, p, "STORE_SCOPE", storeID)
				WriteError(w, r, apperr.New(apperr.Forbidden, "STORE_FORBIDDEN"))
				return
			}
		}
		handler(w, r)
	}))
}

func (h *HumanAuth) deny(r *http.Request, p humanauth.Principal, reason, subject string) {
	if !unsafeMethod(r.Method) {
		return
	}
	h.Svc.Audit(r.Context(), humanauth.AuditEvent{ActorUserID: p.UserID, Action: "authz.denied", Outcome: "denied",
		Reason: reason, ClientIP: h.clientIP(r), Details: map[string]any{"route": r.Pattern, "subject": subject}})
}

// RequireStore checks Store access inside a ScopeHandler handler. It
// writes the response and returns false when access is denied.
func RequireStore(w http.ResponseWriter, r *http.Request, storeID string) bool {
	p, ok := PrincipalOf(r)
	if !ok || !p.CanAccessStore(storeID) {
		WriteError(w, r, apperr.New(apperr.Forbidden, "STORE_FORBIDDEN"))
		return false
	}
	return true
}

// RequireAllStores is RequireStore for cross-Store resources.
func RequireAllStores(w http.ResponseWriter, r *http.Request) bool {
	p, ok := PrincipalOf(r)
	if !ok || !p.AllStores {
		WriteError(w, r, apperr.New(apperr.Forbidden, "STORE_FORBIDDEN"))
		return false
	}
	return true
}

// authError maps domain errors to generic responses. Login failures never
// reveal which check failed.
func authError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, humanauth.ErrInvalidCredentials):
		WriteError(w, r, apperr.New(apperr.Unauthorized, "INVALID_CREDENTIALS"))
	case errors.Is(err, humanauth.ErrThrottled):
		w.Header().Set("Retry-After", "60")
		WriteError(w, r, apperr.New(apperr.TooManyRequests, "RATE_LIMITED"))
	case errors.Is(err, humanauth.ErrInvalidMFACode):
		WriteError(w, r, apperr.New(apperr.Unauthorized, "INVALID_MFA_CODE"))
	case errors.Is(err, humanauth.ErrUnauthenticated):
		WriteError(w, r, apperr.New(apperr.Unauthorized, "AUTH_REQUIRED"))
	case errors.Is(err, humanauth.ErrStage):
		WriteError(w, r, apperr.New(apperr.Forbidden, "MFA_REQUIRED"))
	case errors.Is(err, humanauth.ErrForbidden):
		WriteError(w, r, apperr.New(apperr.Forbidden, "FORBIDDEN"))
	case errors.Is(err, humanauth.ErrNotFound):
		WriteError(w, r, apperr.New(apperr.NotFound, "NOT_FOUND"))
	case errors.Is(err, humanauth.ErrLastOwner):
		WriteError(w, r, apperr.New(apperr.Conflict, "LAST_OWNER"))
	case errors.Is(err, humanauth.ErrLoginTaken):
		WriteError(w, r, apperr.New(apperr.Conflict, "LOGIN_TAKEN"))
	case errors.Is(err, humanauth.ErrConflict), errors.Is(err, humanauth.ErrMFAAlreadyEnabled):
		WriteError(w, r, apperr.New(apperr.Conflict, "STATE_CONFLICT"))
	case errors.Is(err, humanauth.ErrWeakPassword):
		WriteError(w, r, apperr.New(apperr.InvalidInput, "WEAK_PASSWORD"))
	case errors.Is(err, humanauth.ErrInvalidLogin), errors.Is(err, humanauth.ErrInvalidInput):
		WriteError(w, r, apperr.New(apperr.InvalidInput, "INVALID_INPUT"))
	case errors.Is(err, humanauth.ErrActivationInvalid), errors.Is(err, humanauth.ErrMFANotStarted):
		WriteError(w, r, apperr.New(apperr.InvalidInput, "ACTIVATION_INVALID"))
	default:
		WriteError(w, r, apperr.Wrap(apperr.Internal, "internal error", err))
	}
}

// decodeAuthBody strictly decodes a small JSON body.
func decodeAuthBody(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(raw) > 8192 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "INVALID_INPUT"))
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil || dec.More() {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "INVALID_INPUT"))
		return false
	}
	return true
}

func (h *HumanAuth) sessionResponse(w http.ResponseWriter, s humanauth.IssuedSession, extra map[string]any) {
	h.setCookie(w, s)
	body := map[string]any{"stage": string(s.Stage), "csrf_token": s.CSRF}
	for k, v := range extra {
		body[k] = v
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}

// Login: POST /api/v1/dashboard/auth/login {login, password}. Public (no
// session yet) but same-origin only. Generic failure for every cause.
func (h *HumanAuth) Login(w http.ResponseWriter, r *http.Request) {
	if !requireOrigin(r) {
		WriteError(w, r, apperr.New(apperr.Forbidden, "CSRF_REJECTED"))
		return
	}
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	res, err := h.Svc.Login(r.Context(), req.Login, req.Password, h.clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	h.sessionResponse(w, res.Session, nil)
}

// Activate: POST /api/v1/dashboard/auth/activate {token, password}.
func (h *HumanAuth) Activate(w http.ResponseWriter, r *http.Request) {
	if !requireOrigin(r) {
		WriteError(w, r, apperr.New(apperr.Forbidden, "CSRF_REJECTED"))
		return
	}
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	s, err := h.Svc.Activate(r.Context(), req.Token, req.Password, h.clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	h.sessionResponse(w, s, nil)
}

// Me reports the current identity and what the UI needs to gate itself.
// Never returns credential material other than the CSRF token.
func (h *HumanAuth) Me(w http.ResponseWriter, r *http.Request) {
	a, _ := AuthOf(r)
	p := a.Principal
	perms := []string{}
	if a.Session.Stage == humanauth.StageFull {
		perms = humanauth.PermissionsOf(p.Role)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"stage":         string(a.Session.Stage),
		"csrf_token":    h.Svc.CSRFToken(a.Session.ID),
		"user": map[string]any{"id": p.UserID, "login": p.Login, "display_name": p.DisplayName, "role": string(p.Role),
			"mfa_enabled": a.Session.User.MFAEnabled},
		"permissions": perms,
		"all_stores":  p.AllStores,
		"store_ids":   p.StoreIDs(),
		"session":     map[string]any{"expires_at": a.Session.AbsoluteExpiresAt, "idle_timeout_seconds": int(h.Svc.Policy().IdleTimeout / time.Second)},
	})
}

// Logout revokes the server session and clears the cookie.
func (h *HumanAuth) Logout(w http.ResponseWriter, r *http.Request) {
	a, _ := AuthOf(r)
	if err := h.Svc.Logout(r.Context(), a); err != nil {
		authError(w, r, err)
		return
	}
	h.clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

// VerifyMFA: POST .../auth/mfa/verify {code} or {recovery_code}.
func (h *HumanAuth) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	a, _ := AuthOf(r)
	var req struct {
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	s, err := h.Svc.VerifyMFA(r.Context(), a, req.Code, req.RecoveryCode, h.clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	h.sessionResponse(w, s, nil)
}

// StartEnrollment: POST .../auth/mfa/enroll/start.
func (h *HumanAuth) StartEnrollment(w http.ResponseWriter, r *http.Request) {
	a, _ := AuthOf(r)
	e, err := h.Svc.StartEnrollment(r.Context(), a)
	if err != nil {
		authError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, e)
}

// ConfirmEnrollment: POST .../auth/mfa/enroll/confirm {code}.
func (h *HumanAuth) ConfirmEnrollment(w http.ResponseWriter, r *http.Request) {
	a, _ := AuthOf(r)
	var req struct {
		Code string `json:"code"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	s, codes, err := h.Svc.ConfirmEnrollment(r.Context(), a, req.Code, h.clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	h.sessionResponse(w, s, map[string]any{"recovery_codes": codes})
}

// RegenerateRecoveryCodes: POST .../auth/mfa/recovery-codes.
func (h *HumanAuth) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	a, _ := AuthOf(r)
	codes, err := h.Svc.RegenerateRecoveryCodes(r.Context(), a)
	if err != nil {
		authError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// ChangePassword: POST .../auth/password {current_password, new_password}.
func (h *HumanAuth) ChangePassword(w http.ResponseWriter, r *http.Request) {
	a, _ := AuthOf(r)
	var req struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	s, err := h.Svc.ChangePassword(r.Context(), a, req.Current, req.Next)
	if err != nil {
		authError(w, r, err)
		return
	}
	h.sessionResponse(w, s, nil)
}

// userView never includes credential material.
func userView(u humanauth.User) map[string]any {
	stores := u.Stores
	if stores == nil {
		stores = []string{}
	}
	return map[string]any{"id": u.ID, "login": u.Login, "display_name": u.DisplayName, "role": string(u.Role),
		"status": string(u.Status), "all_stores": u.AllStores, "store_ids": stores, "mfa_enabled": u.MFAEnabled,
		"created_at": u.CreatedAt, "updated_at": u.UpdatedAt}
}

// ListUsers: GET /api/v1/dashboard/users?after=&limit=.
func (h *HumanAuth) ListUsers(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalOf(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	users, next, err := h.Svc.ListUsers(r.Context(), p, r.URL.Query().Get("after"), limit)
	if err != nil {
		authError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, userView(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "next_cursor": next})
}

// CreateUser: POST /api/v1/dashboard/users. Returns the one-time
// activation token exactly once.
func (h *HumanAuth) CreateUser(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalOf(r)
	var req struct {
		Login       string   `json:"login"`
		DisplayName string   `json:"display_name"`
		Role        string   `json:"role"`
		AllStores   bool     `json:"all_stores"`
		StoreIDs    []string `json:"store_ids"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	created, err := h.Svc.CreateUser(r.Context(), p, humanauth.NewUserRequest{Login: req.Login, DisplayName: req.DisplayName,
		Role: humanauth.Role(req.Role), AllStores: req.AllStores, Stores: req.StoreIDs})
	if err != nil {
		authError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"user": userView(created.User), "activation_token": created.ActivationToken})
}

// UserAction: POST /api/v1/dashboard/users/{id}/{action} with action in
// activation | role | status | memberships | mfa-reset | sessions-revoke.
func (h *HumanAuth) UserAction(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalOf(r)
	id := r.PathValue("id")
	switch r.PathValue("action") {
	case "activation":
		token, err := h.Svc.ReissueActivation(r.Context(), p, id)
		if err != nil {
			authError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"activation_token": token})
	case "role":
		var req struct {
			Role string `json:"role"`
		}
		if !decodeAuthBody(w, r, &req) {
			return
		}
		u, err := h.Svc.ChangeRole(r.Context(), p, id, humanauth.Role(req.Role))
		h.userResult(w, r, u, err)
	case "status":
		var req struct {
			Status string `json:"status"`
		}
		if !decodeAuthBody(w, r, &req) {
			return
		}
		u, err := h.Svc.SetStatus(r.Context(), p, id, humanauth.Status(req.Status))
		h.userResult(w, r, u, err)
	case "memberships":
		var req struct {
			AllStores bool     `json:"all_stores"`
			StoreIDs  []string `json:"store_ids"`
		}
		if !decodeAuthBody(w, r, &req) {
			return
		}
		u, err := h.Svc.SetMemberships(r.Context(), p, id, req.AllStores, req.StoreIDs)
		h.userResult(w, r, u, err)
	case "mfa-reset":
		if err := h.Svc.ResetUserMFA(r.Context(), p, id); err != nil {
			authError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mfa_reset": true})
	case "sessions-revoke":
		n, err := h.Svc.RevokeSessions(r.Context(), p, id)
		if err != nil {
			authError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
	default:
		WriteError(w, r, apperr.New(apperr.NotFound, "NOT_FOUND"))
	}
}

func (h *HumanAuth) userResult(w http.ResponseWriter, r *http.Request, u humanauth.User, err error) {
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userView(u))
}

// AuthAudit: GET /api/v1/dashboard/auth-audit?before=&limit=.
func (h *HumanAuth) AuthAudit(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalOf(r)
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, next, err := h.Svc.ListAudit(r.Context(), p, before, limit)
	if err != nil {
		authError(w, r, err)
		return
	}
	if rows == nil {
		rows = []humanauth.AuditRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": rows, "next_before": next})
}

// AuthRoutes registers the authentication endpoints.
func (h *HumanAuth) AuthRoutes(mux *http.ServeMux) {
	any3 := []humanauth.Stage{humanauth.StageMFAPending, humanauth.StageMFASetup, humanauth.StageFull}
	mux.HandleFunc("POST /api/v1/dashboard/auth/login", h.Login)
	mux.HandleFunc("POST /api/v1/dashboard/auth/activate", h.Activate)
	mux.Handle("GET /api/v1/dashboard/auth/me", h.session(any3, http.HandlerFunc(h.Me)))
	mux.Handle("POST /api/v1/dashboard/auth/logout", h.session(any3, http.HandlerFunc(h.Logout)))
	mux.Handle("POST /api/v1/dashboard/auth/mfa/verify",
		h.session([]humanauth.Stage{humanauth.StageMFAPending}, http.HandlerFunc(h.VerifyMFA)))
	mux.Handle("POST /api/v1/dashboard/auth/mfa/enroll/start",
		h.session([]humanauth.Stage{humanauth.StageMFASetup}, http.HandlerFunc(h.StartEnrollment)))
	mux.Handle("POST /api/v1/dashboard/auth/mfa/enroll/confirm",
		h.session([]humanauth.Stage{humanauth.StageMFASetup}, http.HandlerFunc(h.ConfirmEnrollment)))
	mux.Handle("POST /api/v1/dashboard/auth/mfa/recovery-codes",
		h.session([]humanauth.Stage{humanauth.StageFull}, http.HandlerFunc(h.RegenerateRecoveryCodes)))
	mux.Handle("POST /api/v1/dashboard/auth/password",
		h.session([]humanauth.Stage{humanauth.StageFull}, http.HandlerFunc(h.ChangePassword)))
	mux.Handle("GET /api/v1/dashboard/users", h.Guard(humanauth.PermUsersRead, ScopeNone, h.ListUsers))
	mux.Handle("POST /api/v1/dashboard/users", h.Guard(humanauth.PermUsersManage, ScopeNone, h.CreateUser))
	mux.Handle("POST /api/v1/dashboard/users/{id}/{action}", h.Guard(humanauth.PermUsersRead, ScopeNone, h.UserAction))
	mux.Handle("GET /api/v1/dashboard/auth-audit", h.Guard(humanauth.PermUsersRead, ScopeNone, h.AuthAudit))
}
