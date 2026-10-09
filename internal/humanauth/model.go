package humanauth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Session stages. A session that has not completed MFA can only reach the
// MFA endpoints, its own identity and logout.
type Stage string

const (
	StageMFAPending Stage = "MFA_PENDING" // password ok, TOTP/recovery challenge required
	StageMFASetup   Stage = "MFA_SETUP"   // password ok, TOTP enrollment required
	StageFull       Stage = "FULL"        // fully authenticated
)

// Policy holds the bounded, configurable session/throttle settings.
type Policy struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// ChallengeTimeout bounds MFA_PENDING / MFA_SETUP sessions.
	ChallengeTimeout time.Duration
	ActivationTTL    time.Duration
	// Account throttle: AccountMaxFailures within ThrottleWindow locks the
	// account key for AccountLock, doubling per consecutive lock up to
	// AccountLockMax. IP throttle likewise with IPMaxFailures / IPLock.
	ThrottleWindow     time.Duration
	AccountMaxFailures int
	AccountLock        time.Duration
	AccountLockMax     time.Duration
	IPMaxFailures      int
	IPLock             time.Duration
	// TouchInterval bounds last_seen_at writes.
	TouchInterval time.Duration
}

// Session bounds (ADR-0053). Values outside the bounds are rejected by
// configuration validation.
const (
	MinIdleTimeout     = 5 * time.Minute
	MaxIdleTimeout     = 8 * time.Hour
	MinAbsoluteTimeout = 1 * time.Hour
	MaxAbsoluteTimeout = 7 * 24 * time.Hour
)

// DefaultPolicy is the production default.
func DefaultPolicy() Policy {
	return Policy{
		IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 12 * time.Hour,
		ChallengeTimeout: 10 * time.Minute, ActivationTTL: 24 * time.Hour,
		ThrottleWindow: 15 * time.Minute, AccountMaxFailures: 5,
		AccountLock: 15 * time.Minute, AccountLockMax: time.Hour,
		IPMaxFailures: 30, IPLock: 15 * time.Minute,
		TouchInterval: time.Minute,
	}
}

// Validate enforces the bounds.
func (p Policy) Validate() error {
	if p.IdleTimeout < MinIdleTimeout || p.IdleTimeout > MaxIdleTimeout {
		return errors.New("AUTH_SESSION_IDLE_TIMEOUT must be within [5m, 8h]")
	}
	if p.AbsoluteTimeout < MinAbsoluteTimeout || p.AbsoluteTimeout > MaxAbsoluteTimeout {
		return errors.New("AUTH_SESSION_ABSOLUTE_TIMEOUT must be within [1h, 168h]")
	}
	if p.IdleTimeout > p.AbsoluteTimeout {
		return errors.New("idle timeout must not exceed absolute timeout")
	}
	return nil
}

// Domain errors. Handlers map them to generic responses; login failures in
// particular never reveal which check failed.
var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrThrottled           = errors.New("too many attempts")
	ErrUnauthenticated     = errors.New("authentication required")
	ErrStage               = errors.New("session stage does not allow this operation")
	ErrForbidden           = errors.New("not authorized")
	ErrNotFound            = errors.New("not found")
	ErrAlreadyBootstrapped = errors.New("an owner has already been bootstrapped")
	ErrLastOwner           = errors.New("at least one active OWNER must remain")
	ErrLoginTaken          = errors.New("login already exists")
	ErrInvalidLogin        = errors.New("invalid login identifier")
	ErrInvalidInput        = errors.New("invalid input")
	ErrMFAAlreadyEnabled   = errors.New("mfa already enabled")
	ErrMFANotStarted       = errors.New("mfa enrollment not started")
	ErrInvalidMFACode      = errors.New("invalid mfa code")
	ErrActivationInvalid   = errors.New("activation token invalid or expired")
	ErrConflict            = errors.New("conflicting change")
)

var loginPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+-]{2,253}$`)

// NormalizeLogin trims and lower-cases a login identifier and validates it.
func NormalizeLogin(login string) (string, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if !loginPattern.MatchString(login) {
		return "", ErrInvalidLogin
	}
	return login, nil
}

// ValidDisplayName bounds display names.
func ValidDisplayName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && len([]rune(name)) <= 100
}

// User is the stored human account.
type User struct {
	ID              string
	Login           string
	DisplayName     string
	Role            Role
	Status          Status
	AllStores       bool
	PasswordHash    string // empty while PENDING_SETUP without a password
	SecurityVersion int64
	MFAEnabled      bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Stores          []string
}

// SessionRecord is a stored session joined with its user's current state.
type SessionRecord struct {
	ID                string
	UserID            string
	Stage             Stage
	SecurityVersion   int64
	CSRFHash          []byte
	CreatedAt         time.Time
	LastSeenAt        time.Time
	AbsoluteExpiresAt time.Time
	AuthTime          time.Time
	MFAVerifiedAt     *time.Time
	Revoked           bool
	User              User
}

// NewSession is a session to insert.
type NewSession struct {
	ID                string
	TokenHash         []byte
	CSRFHash          []byte
	UserID            string
	Stage             Stage
	SecurityVersion   int64
	CreatedAt         time.Time
	AbsoluteExpiresAt time.Time
	AuthTime          time.Time
	MFAVerifiedAt     *time.Time
}

// MFARecord is a stored TOTP credential.
type MFARecord struct {
	Ciphertext   []byte
	KeyVersion   int
	Enabled      bool
	LastUsedStep uint64
}

// ThrottleKey identifies a throttled identity.
type ThrottleKey struct {
	Kind string // "account" | "ip"
	Key  string
}

// AuditEvent is one append-only security audit row. Never carries
// secrets: no password, TOTP secret/code, recovery code or token.
type AuditEvent struct {
	OccurredAt   time.Time
	ActorUserID  string
	TargetUserID string
	Action       string
	Outcome      string // success | failure | denied
	Reason       string
	ClientIP     string
	Details      map[string]any
}

// AuditRow is a listed audit event.
type AuditRow struct {
	ID           int64          `json:"id"`
	OccurredAt   time.Time      `json:"occurred_at"`
	ActorUserID  string         `json:"actor_user_id,omitempty"`
	TargetUserID string         `json:"target_user_id,omitempty"`
	Action       string         `json:"action"`
	Outcome      string         `json:"outcome"`
	Reason       string         `json:"reason,omitempty"`
	ClientIP     string         `json:"client_ip,omitempty"`
	Details      map[string]any `json:"details"`
}

// UserChange is an administrative account change applied under the
// transactional OWNER-preservation guard.
type UserChange struct {
	Role      *Role
	Status    *Status
	AllStores *bool
	Stores    *[]string
	// BumpSecurity invalidates the target's existing sessions.
	BumpSecurity bool
}

// Store is the persistence contract (PostgreSQL adapter). Every method is
// one transaction; multi-row invariants are enforced inside it.
type Store interface {
	Bootstrap(ctx context.Context, owner User, now time.Time) error
	UserByLogin(ctx context.Context, login string) (User, error)
	UserByID(ctx context.Context, id string) (User, error)
	ListUsers(ctx context.Context, afterLogin string, limit int) ([]User, error)
	CreateUser(ctx context.Context, user User, createdBy string, activationHash []byte, expiresAt, now time.Time) error
	ChangeUser(ctx context.Context, userID string, change UserChange, now time.Time) (User, error)
	SetPassword(ctx context.Context, userID, passwordHash string, bumpSecurity bool, now time.Time) (int64, error)
	RehashPassword(ctx context.Context, userID, oldHash, newHash string, now time.Time) error
	IssueActivation(ctx context.Context, userID, createdBy string, tokenHash []byte, expiresAt, now time.Time) error
	ConsumeActivation(ctx context.Context, tokenHash []byte, passwordHash string, now time.Time) (string, int64, error)

	CheckThrottle(ctx context.Context, keys []ThrottleKey, now time.Time) (bool, error)
	RecordFailure(ctx context.Context, keys []ThrottleKey, policy Policy, now time.Time) error
	ClearThrottle(ctx context.Context, key ThrottleKey) error

	CreateSession(ctx context.Context, s NewSession) error
	RotateSession(ctx context.Context, oldID, reason string, s NewSession, now time.Time) error
	SessionByTokenHash(ctx context.Context, hash []byte) (SessionRecord, error)
	TouchSession(ctx context.Context, id string, now time.Time) error
	RevokeSession(ctx context.Context, id, reason string, now time.Time) (bool, error)
	RevokeUserSessions(ctx context.Context, userID, exceptID, reason string, now time.Time) (int, error)

	GetMFA(ctx context.Context, userID string) (MFARecord, error)
	PutPendingMFA(ctx context.Context, userID string, ciphertext []byte, keyVersion int, now time.Time) error
	EnableMFA(ctx context.Context, userID string, step uint64, codeHashes [][]byte, now time.Time) (int64, error)
	ConsumeTOTPStep(ctx context.Context, userID string, step uint64) (bool, error)
	ConsumeRecoveryCode(ctx context.Context, userID string, codeHash []byte, now time.Time) (bool, error)
	ReplaceRecoveryCodes(ctx context.Context, userID string, codeHashes [][]byte, now time.Time) error
	ResetMFA(ctx context.Context, userID string, now time.Time) (int64, error)
	RemainingRecoveryCodes(ctx context.Context, userID string) (int, error)

	Audit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, beforeID int64, limit int) ([]AuditRow, error)
}
