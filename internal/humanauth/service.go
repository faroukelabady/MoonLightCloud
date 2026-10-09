package humanauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// MFAIssuer labels the authenticator entry.
const MFAIssuer = "MoonLight Cloud"

// Service implements human authentication and account administration.
type Service struct {
	store  Store
	box    *SecretBox
	clock  clock.Clock
	ids    ids.Generator
	policy Policy
	argon  Argon2Params
	log    *slog.Logger
	// csrfKey derives each session's CSRF token as HMAC(csrfKey, session
	// ID), so /me can re-issue it after a page reload while only a digest
	// is stored.
	csrfKey []byte
}

// NewService wires the service. box is required: every privileged account
// uses MFA, so a missing encryption key is a configuration error upstream.
func NewService(store Store, box *SecretBox, csrfKey []byte, clk clock.Clock, gen ids.Generator, policy Policy, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, box: box, csrfKey: csrfKey, clock: clk, ids: gen, policy: policy, argon: CurrentArgon2, log: log}
}

// CSRFToken is the session's CSRF token (HMAC of the session ID).
func (s *Service) CSRFToken(sessionID string) string {
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write([]byte("moonlight-cloud/csrf/v1:" + sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// WithArgon2 overrides hashing parameters (tests use cheaper settings to
// keep suites fast; production always uses CurrentArgon2).
func (s *Service) WithArgon2(p Argon2Params) *Service { s.argon = p; return s }

// Policy returns the active policy.
func (s *Service) Policy() Policy { return s.policy }

func (s *Service) now() time.Time { return s.clock.Now().UTC() }

func (s *Service) audit(ctx context.Context, e AuditEvent) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = s.now()
	}
	if err := s.store.Audit(ctx, e); err != nil {
		// Never fatal for the request, never containing secrets.
		s.log.Error("auth audit write failed", "action", e.Action, "error", err)
	}
}

// Audit records an externally detected security event (authorization
// denials on sensitive routes).
func (s *Service) Audit(ctx context.Context, e AuditEvent) { s.audit(ctx, e) }

// IssuedSession is a freshly minted browser session. Token and CSRF are
// returned exactly once and stored only as digests.
type IssuedSession struct {
	ID        string
	Token     string
	CSRF      string
	Stage     Stage
	ExpiresAt time.Time
}

func (s *Service) newSession(userID string, stage Stage, version int64, authTime time.Time, mfaAt *time.Time) (IssuedSession, NewSession, error) {
	token, tokenHash, err := NewToken()
	if err != nil {
		return IssuedSession{}, NewSession{}, err
	}
	id := s.ids.New()
	csrf := s.CSRFToken(id)
	csrfHash := HashToken(csrf)
	now := s.now()
	ttl := s.policy.AbsoluteTimeout
	if stage != StageFull {
		ttl = s.policy.ChallengeTimeout
	}
	// A rotated session never outlives the original authentication.
	expires := now.Add(ttl)
	if limit := authTime.Add(s.policy.AbsoluteTimeout); stage == StageFull && expires.After(limit) {
		expires = limit
	}
	return IssuedSession{ID: id, Token: token, CSRF: csrf, Stage: stage, ExpiresAt: expires},
		NewSession{ID: id, TokenHash: tokenHash, CSRFHash: csrfHash, UserID: userID, Stage: stage,
			SecurityVersion: version, CreatedAt: now, AbsoluteExpiresAt: expires, AuthTime: authTime, MFAVerifiedAt: mfaAt}, nil
}

// BootstrapOwner creates the first OWNER exactly once (server-side CLI).
func (s *Service) BootstrapOwner(ctx context.Context, login, displayName, password string, stores []string) (User, error) {
	login, err := NormalizeLogin(login)
	if err != nil {
		return User{}, err
	}
	if !ValidDisplayName(displayName) {
		return User{}, ErrInvalidInput
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password, s.argon)
	if err != nil {
		return User{}, err
	}
	now := s.now()
	owner := User{ID: s.ids.New(), Login: login, DisplayName: displayName, Role: RoleOwner, Status: StatusActive,
		AllStores: len(stores) == 0, Stores: stores, PasswordHash: hash, SecurityVersion: 1}
	if err := s.store.Bootstrap(ctx, owner, now); err != nil {
		return User{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, TargetUserID: owner.ID, Action: "auth.bootstrap_owner", Outcome: "success"})
	return owner, nil
}

// LoginResult is the outcome of primary authentication.
type LoginResult struct {
	Session IssuedSession
	UserID  string
}

// Login performs primary (password) authentication. Every failure,
// including unknown account, disabled account, missing password and
// throttling, is reported to the caller only as ErrInvalidCredentials or
// ErrThrottled. A successful login never yields a FULL session directly:
// MFA is always required (verify, or enroll first).
func (s *Service) Login(ctx context.Context, login, password, clientIP string) (LoginResult, error) {
	now := s.now()
	normalized, loginErr := NormalizeLogin(login)
	accountKey := ThrottleKey{Kind: "account", Key: normalized}
	if loginErr != nil {
		accountKey.Key = "invalid"
	}
	keys := []ThrottleKey{accountKey, {Kind: "ip", Key: boundedIP(clientIP)}}
	blocked, err := s.store.CheckThrottle(ctx, keys, now)
	if err != nil {
		return LoginResult{}, err
	}
	if blocked {
		s.audit(ctx, AuditEvent{OccurredAt: now, Action: "auth.login", Outcome: "failure", Reason: "THROTTLED", ClientIP: boundedIP(clientIP)})
		return LoginResult{}, ErrThrottled
	}
	fail := func(reason, targetID string) (LoginResult, error) {
		if err := s.store.RecordFailure(ctx, keys, s.policy, now); err != nil {
			return LoginResult{}, err
		}
		s.audit(ctx, AuditEvent{OccurredAt: now, TargetUserID: targetID, Action: "auth.login", Outcome: "failure", Reason: reason, ClientIP: boundedIP(clientIP)})
		return LoginResult{}, ErrInvalidCredentials
	}
	if loginErr != nil || len(password) > MaxPasswordBytes {
		burnPasswordCheck(password)
		return fail("INVALID_CREDENTIALS", "")
	}
	user, err := s.store.UserByLogin(ctx, normalized)
	if errors.Is(err, ErrNotFound) {
		burnPasswordCheck(password)
		return fail("INVALID_CREDENTIALS", "")
	}
	if err != nil {
		return LoginResult{}, err
	}
	if user.Status == StatusDisabled || user.PasswordHash == "" {
		burnPasswordCheck(password)
		return fail("INVALID_CREDENTIALS", user.ID)
	}
	if !VerifyPassword(password, user.PasswordHash) {
		return fail("INVALID_CREDENTIALS", user.ID)
	}
	if err := s.store.ClearThrottle(ctx, accountKey); err != nil {
		return LoginResult{}, err
	}
	if NeedsRehash(user.PasswordHash, s.argon) {
		if fresh, err := HashPassword(password, s.argon); err == nil {
			if err := s.store.RehashPassword(ctx, user.ID, user.PasswordHash, fresh, now); err != nil {
				s.log.Warn("password rehash skipped", "error", err)
			}
		}
	}
	stage := StageMFASetup
	if user.MFAEnabled {
		stage = StageMFAPending
	}
	issued, row, err := s.newSession(user.ID, stage, user.SecurityVersion, now, nil)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.store.CreateSession(ctx, row); err != nil {
		return LoginResult{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: user.ID, TargetUserID: user.ID, Action: "auth.login",
		Outcome: "success", Reason: "PRIMARY_" + string(stage), ClientIP: boundedIP(clientIP)})
	return LoginResult{Session: issued, UserID: user.ID}, nil
}

func boundedIP(ip string) string {
	if ip == "" {
		return "unknown"
	}
	if len(ip) > 64 {
		return ip[:64]
	}
	return ip
}

// Authenticated is a validated session with its current principal.
type Authenticated struct {
	Session   SessionRecord
	Principal Principal
}

// Authenticate validates a browser token against CURRENT server state:
// unknown, revoked, idle-expired, absolutely expired, security-version
// stale or disabled-user sessions are all rejected identically.
func (s *Service) Authenticate(ctx context.Context, token string) (Authenticated, error) {
	if token == "" || len(token) > 128 {
		return Authenticated{}, ErrUnauthenticated
	}
	rec, err := s.store.SessionByTokenHash(ctx, HashToken(token))
	if err != nil {
		return Authenticated{}, ErrUnauthenticated
	}
	now := s.now()
	switch {
	case rec.Revoked,
		!now.Before(rec.AbsoluteExpiresAt),
		!now.Before(rec.LastSeenAt.Add(s.policy.IdleTimeout)),
		rec.SecurityVersion != rec.User.SecurityVersion,
		rec.User.Status == StatusDisabled,
		rec.User.Status == StatusPendingSetup && rec.Stage != StageMFASetup:
		return Authenticated{}, ErrUnauthenticated
	}
	if now.Sub(rec.LastSeenAt) >= s.policy.TouchInterval {
		if err := s.store.TouchSession(ctx, rec.ID, now); err != nil {
			return Authenticated{}, err
		}
	}
	stores := make(map[string]bool, len(rec.User.Stores))
	for _, id := range rec.User.Stores {
		stores[id] = true
	}
	p := Principal{UserID: rec.User.ID, Login: rec.User.Login, DisplayName: rec.User.DisplayName, Role: rec.User.Role,
		AllStores: rec.User.AllStores, Stores: stores, SessionID: rec.ID, AuthTime: rec.AuthTime.Unix()}
	if rec.MFAVerifiedAt != nil {
		p.MFAVerified = rec.MFAVerifiedAt.Unix()
	}
	return Authenticated{Session: rec, Principal: p}, nil
}

// CheckCSRF compares a request CSRF token with the session's digest.
func CheckCSRF(rec SessionRecord, token string) bool {
	if token == "" || len(token) > 128 {
		return false
	}
	return subtle.ConstantTimeCompare(HashToken(token), rec.CSRFHash) == 1
}

// VerifyMFA completes an MFA_PENDING session with a TOTP code or a
// one-time recovery code and rotates it to a FULL session.
func (s *Service) VerifyMFA(ctx context.Context, a Authenticated, code, recoveryCode, clientIP string) (IssuedSession, error) {
	if a.Session.Stage != StageMFAPending {
		return IssuedSession{}, ErrStage
	}
	now := s.now()
	userID := a.Session.UserID
	keys := []ThrottleKey{{Kind: "account", Key: a.Session.User.Login}, {Kind: "ip", Key: boundedIP(clientIP)}}
	if blocked, err := s.store.CheckThrottle(ctx, keys, now); err != nil {
		return IssuedSession{}, err
	} else if blocked {
		s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: userID, TargetUserID: userID, Action: "auth.mfa.verify", Outcome: "failure", Reason: "THROTTLED", ClientIP: boundedIP(clientIP)})
		return IssuedSession{}, ErrThrottled
	}
	ok, method := false, "totp"
	if recoveryCode != "" {
		method = "recovery_code"
		consumed, err := s.store.ConsumeRecoveryCode(ctx, userID, HashRecoveryCode(recoveryCode), now)
		if err != nil {
			return IssuedSession{}, err
		}
		ok = consumed
	} else {
		mfa, err := s.store.GetMFA(ctx, userID)
		if err != nil || !mfa.Enabled {
			return IssuedSession{}, ErrInvalidMFACode
		}
		secret, err := s.box.Open(userID, mfa.Ciphertext)
		if err != nil {
			s.audit(ctx, AuditEvent{OccurredAt: now, TargetUserID: userID, Action: "auth.mfa.verify", Outcome: "failure", Reason: "SECRET_UNREADABLE"})
			return IssuedSession{}, ErrInvalidMFACode
		}
		if step, matched := VerifyTOTP(secret, code, now, mfa.LastUsedStep); matched {
			ok, err = s.store.ConsumeTOTPStep(ctx, userID, step)
			if err != nil {
				return IssuedSession{}, err
			}
		}
	}
	if !ok {
		if err := s.store.RecordFailure(ctx, keys, s.policy, now); err != nil {
			return IssuedSession{}, err
		}
		s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: userID, TargetUserID: userID, Action: "auth.mfa.verify", Outcome: "failure",
			Reason: "INVALID_CODE", ClientIP: boundedIP(clientIP), Details: map[string]any{"method": method}})
		return IssuedSession{}, ErrInvalidMFACode
	}
	if err := s.store.ClearThrottle(ctx, keys[0]); err != nil {
		return IssuedSession{}, err
	}
	issued, row, err := s.newSession(userID, StageFull, a.Session.User.SecurityVersion, a.Session.AuthTime, &now)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := s.store.RotateSession(ctx, a.Session.ID, "mfa_verified", row, now); err != nil {
		return IssuedSession{}, err
	}
	action := "auth.mfa.verify"
	details := map[string]any{"method": method}
	if method == "recovery_code" {
		action = "auth.mfa.recovery_used"
		if left, err := s.store.RemainingRecoveryCodes(ctx, userID); err == nil {
			details["remaining"] = left
		}
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: userID, TargetUserID: userID, Action: action, Outcome: "success", ClientIP: boundedIP(clientIP), Details: details})
	return issued, nil
}

// Enrollment is the TOTP provisioning material, shown to the user once.
type Enrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"otpauth_uri"`
}

// StartEnrollment generates (or replaces) a pending TOTP secret for an
// MFA_SETUP session. An enabled credential is never replaced here.
func (s *Service) StartEnrollment(ctx context.Context, a Authenticated) (Enrollment, error) {
	if a.Session.Stage != StageMFASetup {
		return Enrollment{}, ErrStage
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return Enrollment{}, err
	}
	sealed, err := s.box.Seal(a.Session.UserID, secret)
	if err != nil {
		return Enrollment{}, err
	}
	if err := s.store.PutPendingMFA(ctx, a.Session.UserID, sealed, s.box.KeyVersion(), s.now()); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{Secret: EncodeTOTPSecret(secret), URI: TOTPURI(MFAIssuer, a.Session.User.Login, secret)}, nil
}

// ConfirmEnrollment verifies the first code, enables MFA, issues the
// one-time recovery codes, activates a PENDING_SETUP account and rotates
// the session to FULL (all other sessions of the user are invalidated by
// the security-version bump).
func (s *Service) ConfirmEnrollment(ctx context.Context, a Authenticated, code, clientIP string) (IssuedSession, []string, error) {
	if a.Session.Stage != StageMFASetup {
		return IssuedSession{}, nil, ErrStage
	}
	now := s.now()
	userID := a.Session.UserID
	mfa, err := s.store.GetMFA(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return IssuedSession{}, nil, ErrMFANotStarted
	}
	if err != nil {
		return IssuedSession{}, nil, err
	}
	if mfa.Enabled {
		return IssuedSession{}, nil, ErrMFAAlreadyEnabled
	}
	secret, err := s.box.Open(userID, mfa.Ciphertext)
	if err != nil {
		return IssuedSession{}, nil, ErrInvalidMFACode
	}
	step, ok := VerifyTOTP(secret, code, now, 0)
	if !ok {
		keys := []ThrottleKey{{Kind: "account", Key: a.Session.User.Login}, {Kind: "ip", Key: boundedIP(clientIP)}}
		if err := s.store.RecordFailure(ctx, keys, s.policy, now); err != nil {
			return IssuedSession{}, nil, err
		}
		return IssuedSession{}, nil, ErrInvalidMFACode
	}
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		return IssuedSession{}, nil, err
	}
	version, err := s.store.EnableMFA(ctx, userID, mfa.Ciphertext, mfa.KeyVersion, step, hashes, now)
	if err != nil {
		return IssuedSession{}, nil, err
	}
	issued, row, err := s.newSession(userID, StageFull, version, a.Session.AuthTime, &now)
	if err != nil {
		return IssuedSession{}, nil, err
	}
	if err := s.store.RotateSession(ctx, a.Session.ID, "mfa_enrolled", row, now); err != nil {
		return IssuedSession{}, nil, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: userID, TargetUserID: userID, Action: "auth.mfa.enrolled", Outcome: "success", ClientIP: boundedIP(clientIP)})
	return issued, codes, nil
}

// RegenerateRecoveryCodes replaces the user's recovery codes (previous
// codes stop working) and returns the new set once.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, a Authenticated) ([]string, error) {
	if a.Session.Stage != StageFull {
		return nil, ErrStage
	}
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	now := s.now()
	if err := s.store.ReplaceRecoveryCodes(ctx, a.Session.UserID, hashes, now); err != nil {
		return nil, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: a.Session.UserID, TargetUserID: a.Session.UserID, Action: "auth.mfa.recovery_regenerated", Outcome: "success"})
	return codes, nil
}

// Logout revokes the server-side session (clearing the cookie alone is
// never sufficient).
func (s *Service) Logout(ctx context.Context, a Authenticated) error {
	now := s.now()
	if _, err := s.store.RevokeSession(ctx, a.Session.ID, "logout", now); err != nil {
		return err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: a.Session.UserID, TargetUserID: a.Session.UserID, Action: "auth.logout", Outcome: "success"})
	return nil
}

// ChangePassword verifies the current password, stores the new one,
// revokes every other session and rotates the current one.
func (s *Service) ChangePassword(ctx context.Context, a Authenticated, current, next string) (IssuedSession, error) {
	if a.Session.Stage != StageFull {
		return IssuedSession{}, ErrStage
	}
	now := s.now()
	if !VerifyPassword(current, a.Session.User.PasswordHash) {
		keys := []ThrottleKey{{Kind: "account", Key: a.Session.User.Login}}
		if err := s.store.RecordFailure(ctx, keys, s.policy, now); err != nil {
			return IssuedSession{}, err
		}
		return IssuedSession{}, ErrInvalidCredentials
	}
	if err := ValidatePassword(next); err != nil {
		return IssuedSession{}, err
	}
	hash, err := HashPassword(next, s.argon)
	if err != nil {
		return IssuedSession{}, err
	}
	version, err := s.store.SetPassword(ctx, a.Session.UserID, hash, true, now)
	if err != nil {
		return IssuedSession{}, err
	}
	issued, row, err := s.newSession(a.Session.UserID, StageFull, version, now, a.Session.MFAVerifiedAt)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := s.store.RotateSession(ctx, a.Session.ID, "password_changed", row, now); err != nil {
		return IssuedSession{}, err
	}
	if _, err := s.store.RevokeUserSessions(ctx, a.Session.UserID, issued.ID, "password_changed", now); err != nil {
		return IssuedSession{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: a.Session.UserID, TargetUserID: a.Session.UserID, Action: "auth.password.changed", Outcome: "success"})
	return issued, nil
}

// ResetPassword is the server-side recovery path (CLI with direct server
// access): sets a new password, optionally clears MFA (the user re-enrolls
// at next login), and revokes every session.
func (s *Service) ResetPassword(ctx context.Context, login, password string, resetMFA bool) (User, error) {
	login, err := NormalizeLogin(login)
	if err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	user, err := s.store.UserByLogin(ctx, login)
	if err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password, s.argon)
	if err != nil {
		return User{}, err
	}
	now := s.now()
	if _, err := s.store.SetPassword(ctx, user.ID, hash, true, now); err != nil {
		return User{}, err
	}
	if resetMFA {
		if _, err := s.store.ResetMFA(ctx, user.ID, now); err != nil {
			return User{}, err
		}
	}
	if _, err := s.store.RevokeUserSessions(ctx, user.ID, "", "password_reset", now); err != nil {
		return User{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, TargetUserID: user.ID, Action: "auth.password.reset", Outcome: "success",
		Details: map[string]any{"via": "server_cli", "mfa_reset": resetMFA}})
	return s.store.UserByID(ctx, user.ID)
}

// Activate consumes a one-time activation token, sets the password and
// returns an MFA_SETUP session: the account becomes ACTIVE only after MFA
// enrollment completes.
func (s *Service) Activate(ctx context.Context, token, password, clientIP string) (IssuedSession, error) {
	if err := ValidatePassword(password); err != nil {
		return IssuedSession{}, err
	}
	if token == "" || len(token) > 128 {
		return IssuedSession{}, ErrActivationInvalid
	}
	hash, err := HashPassword(password, s.argon)
	if err != nil {
		return IssuedSession{}, err
	}
	now := s.now()
	userID, version, err := s.store.ConsumeActivation(ctx, HashToken(token), hash, now)
	if err != nil {
		s.audit(ctx, AuditEvent{OccurredAt: now, Action: "user.activated", Outcome: "failure", Reason: "TOKEN_INVALID", ClientIP: boundedIP(clientIP)})
		return IssuedSession{}, ErrActivationInvalid
	}
	issued, row, err := s.newSession(userID, StageMFASetup, version, now, nil)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := s.store.CreateSession(ctx, row); err != nil {
		return IssuedSession{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: userID, TargetUserID: userID, Action: "user.activated", Outcome: "success", ClientIP: boundedIP(clientIP)})
	return issued, nil
}

// WithClock replaces the application clock (tests drive time
// deterministically: TOTP steps, expiry, throttling).
func (s *Service) WithClock(clk clock.Clock) *Service { s.clock = clk; return s }
