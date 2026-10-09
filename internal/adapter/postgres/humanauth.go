package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
)

// HumanAuth is the PostgreSQL store for human identity (ADR-0053). Every
// method is one transaction; times come from the caller's application
// clock (never now()).
type HumanAuth struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

// NewHumanAuth builds the store.
func NewHumanAuth(pool *pgxpool.Pool, timeout time.Duration) HumanAuth {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return HumanAuth{pool: pool, timeout: timeout}
}

var _ humanauth.Store = HumanAuth{}

func (h HumanAuth) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const userColumns = `u.id::text, u.login, u.display_name, u.role, u.status, u.all_stores, COALESCE(u.password_hash, ''),
	u.security_version, u.created_at, u.updated_at,
	EXISTS (SELECT 1 FROM admin_mfa_credentials m WHERE m.user_id = u.id AND m.enabled_at IS NOT NULL),
	COALESCE((SELECT array_agg(ms.store_id::text ORDER BY ms.store_id) FROM admin_user_store_memberships ms WHERE ms.user_id = u.id), '{}')`

func scanUser(row pgx.Row) (humanauth.User, error) {
	var u humanauth.User
	var role, status string
	err := row.Scan(&u.ID, &u.Login, &u.DisplayName, &role, &status, &u.AllStores, &u.PasswordHash,
		&u.SecurityVersion, &u.CreatedAt, &u.UpdatedAt, &u.MFAEnabled, &u.Stores)
	if errors.Is(err, pgx.ErrNoRows) {
		return humanauth.User{}, humanauth.ErrNotFound
	}
	u.Role, u.Status = humanauth.Role(role), humanauth.Status(status)
	return u, err
}

func insertMemberships(ctx context.Context, tx pgx.Tx, userID string, stores []string, now time.Time) error {
	for _, storeID := range stores {
		if _, err := tx.Exec(ctx, `INSERT INTO admin_user_store_memberships (user_id, store_id, created_at) VALUES ($1, $2, $3)`,
			userID, storeID, now); err != nil {
			if isForeignKeyViolation(err) {
				return humanauth.ErrInvalidInput
			}
			return err
		}
	}
	return nil
}

func isForeignKeyViolation(err error) bool { return pgCode(err) == "23503" }

// Bootstrap inserts the first OWNER and the singleton bootstrap row in one
// transaction: a second (or concurrent) bootstrap fails on the singleton
// key and on the "no humans yet" check under a table lock.
func (h HumanAuth) Bootstrap(ctx context.Context, owner humanauth.User, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE admin_users IN EXCLUSIVE MODE`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM admin_users) OR EXISTS (SELECT 1 FROM admin_bootstrap_state)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return humanauth.ErrAlreadyBootstrapped
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admin_users (id, login, display_name, role, status, all_stores, password_hash,
			password_changed_at, security_version, created_at, updated_at)
			VALUES ($1, $2, $3, 'OWNER', 'ACTIVE', $4, $5, $6, 1, $6, $6)`,
			owner.ID, owner.Login, owner.DisplayName, owner.AllStores, owner.PasswordHash, now); err != nil {
			return err
		}
		if err := insertMemberships(ctx, tx, owner.ID, owner.Stores, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admin_bootstrap_state (singleton, owner_id, bootstrapped_at) VALUES (true, $1, $2)`, owner.ID, now); err != nil {
			if isUniqueViolation(err) {
				return humanauth.ErrAlreadyBootstrapped
			}
			return err
		}
		return nil
	})
}

func (h HumanAuth) UserByLogin(ctx context.Context, login string) (humanauth.User, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	return scanUser(h.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM admin_users u WHERE u.login = $1`, login))
}

func (h HumanAuth) UserByID(ctx context.Context, id string) (humanauth.User, error) {
	if !validUUID(id) {
		return humanauth.User{}, humanauth.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	return scanUser(h.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM admin_users u WHERE u.id = $1`, id))
}

func (h HumanAuth) ListUsers(ctx context.Context, afterLogin string, limit int) ([]humanauth.User, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	rows, err := h.pool.Query(ctx, `SELECT `+userColumns+` FROM admin_users u WHERE u.login > $1 ORDER BY u.login LIMIT $2`, afterLogin, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []humanauth.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (h HumanAuth) CreateUser(ctx context.Context, user humanauth.User, createdBy string, activationHash []byte, expiresAt, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO admin_users (id, login, display_name, role, status, all_stores, security_version, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'PENDING_SETUP', $5, 1, $6, $7, $7)`,
			user.ID, user.Login, user.DisplayName, string(user.Role), user.AllStores, createdBy, now); err != nil {
			if isUniqueViolation(err) {
				return humanauth.ErrLoginTaken
			}
			return err
		}
		if err := insertMemberships(ctx, tx, user.ID, user.Stores, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO admin_activation_tokens (token_hash, user_id, created_by, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
			activationHash, user.ID, createdBy, now, expiresAt)
		return err
	})
}

// ChangeUser applies an administrative change under the OWNER guard: every
// active OWNER row is locked first, so concurrent demote/disable races
// serialize and can never leave zero active OWNERs.
func (h HumanAuth) ChangeUser(ctx context.Context, userID string, change humanauth.UserChange, now time.Time) (humanauth.User, error) {
	if !validUUID(userID) {
		return humanauth.User{}, humanauth.ErrNotFound
	}
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		ownerRows, err := tx.Query(ctx, `SELECT id::text FROM admin_users WHERE role = 'OWNER' AND status = 'ACTIVE' ORDER BY id FOR UPDATE`)
		if err != nil {
			return err
		}
		activeOwners := map[string]bool{}
		for ownerRows.Next() {
			var id string
			if err := ownerRows.Scan(&id); err != nil {
				ownerRows.Close()
				return err
			}
			activeOwners[id] = true
		}
		ownerRows.Close()
		if err := ownerRows.Err(); err != nil {
			return err
		}
		var role, status string
		if err := tx.QueryRow(ctx, `SELECT role, status FROM admin_users WHERE id = $1 FOR UPDATE`, userID).Scan(&role, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return humanauth.ErrNotFound
			}
			return err
		}
		newRole, newStatus := role, status
		if change.Role != nil {
			newRole = string(*change.Role)
		}
		if change.Status != nil {
			newStatus = string(*change.Status)
		}
		wasActiveOwner := role == "OWNER" && status == "ACTIVE"
		staysActiveOwner := newRole == "OWNER" && newStatus == "ACTIVE"
		if wasActiveOwner && !staysActiveOwner && len(activeOwners) <= 1 {
			return humanauth.ErrLastOwner
		}
		bump := 0
		if change.BumpSecurity {
			bump = 1
		}
		allStores := (*bool)(nil)
		if change.AllStores != nil {
			allStores = change.AllStores
		}
		if _, err := tx.Exec(ctx, `UPDATE admin_users SET role = $2, status = $3,
			all_stores = COALESCE($4, all_stores), security_version = security_version + $5, updated_at = $6 WHERE id = $1`,
			userID, newRole, newStatus, allStores, bump, now); err != nil {
			if pgCode(err) == "23514" {
				return humanauth.ErrLastOwner
			}
			return err
		}
		if change.Stores != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM admin_user_store_memberships WHERE user_id = $1`, userID); err != nil {
				return err
			}
			if err := insertMemberships(ctx, tx, userID, *change.Stores, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return humanauth.User{}, err
	}
	return h.UserByID(ctx, userID)
}

func (h HumanAuth) SetPassword(ctx context.Context, userID, passwordHash string, bumpSecurity bool, now time.Time) (int64, error) {
	var version int64
	bump := 0
	if bumpSecurity {
		bump = 1
	}
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE admin_users SET password_hash = $2, password_changed_at = $3, updated_at = $3,
			security_version = security_version + $4 WHERE id = $1 RETURNING security_version`, userID, passwordHash, now, bump).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return humanauth.ErrNotFound
		}
		return err
	})
	return version, err
}

// RehashPassword upgrades parameters only if the stored hash is unchanged
// (compare-and-set: never overwrites a concurrent password change).
func (h HumanAuth) RehashPassword(ctx context.Context, userID, oldHash, newHash string, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE admin_users SET password_hash = $3, updated_at = $4 WHERE id = $1 AND password_hash = $2`,
			userID, oldHash, newHash, now)
		return err
	})
}

func (h HumanAuth) IssueActivation(ctx context.Context, userID, createdBy string, tokenHash []byte, expiresAt, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		// Previous unused tokens stop working.
		if _, err := tx.Exec(ctx, `UPDATE admin_activation_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, userID, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO admin_activation_tokens (token_hash, user_id, created_by, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
			tokenHash, userID, createdBy, now, expiresAt)
		return err
	})
}

// ConsumeActivation spends a one-time token (single winner under
// concurrency: the conditional UPDATE) and sets the first password.
func (h HumanAuth) ConsumeActivation(ctx context.Context, tokenHash []byte, passwordHash string, now time.Time) (string, int64, error) {
	var userID string
	var version int64
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE admin_activation_tokens t SET used_at = $2
			FROM admin_users u
			WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > $2
			  AND u.id = t.user_id AND u.status = 'PENDING_SETUP'
			RETURNING t.user_id::text`, tokenHash, now).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return humanauth.ErrActivationInvalid
		}
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE admin_users SET password_hash = $2, password_changed_at = $3, updated_at = $3,
			security_version = security_version + 1 WHERE id = $1 RETURNING security_version`, userID, passwordHash, now).Scan(&version)
	})
	return userID, version, err
}

// CheckThrottle reports whether any key is currently locked.
func (h HumanAuth) CheckThrottle(ctx context.Context, keys []humanauth.ThrottleKey, now time.Time) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	for _, k := range keys {
		var locked bool
		err := h.pool.QueryRow(ctx, `SELECT locked_until IS NOT NULL AND locked_until > $3 FROM admin_login_throttle WHERE key_kind = $1 AND key = $2`,
			k.Kind, k.Key, now).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if locked {
			return true, nil
		}
	}
	return false, nil
}

// RecordFailure counts one failure per key within the window and applies
// a bounded temporary lock when the threshold is reached. Old rows are
// pruned opportunistically, so attacker-chosen keys cannot grow the table
// without bound.
func (h HumanAuth) RecordFailure(ctx context.Context, keys []humanauth.ThrottleKey, policy humanauth.Policy, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM admin_login_throttle WHERE updated_at < $1 AND (locked_until IS NULL OR locked_until < $2)`,
			now.Add(-24*time.Hour), now); err != nil {
			return err
		}
		for _, k := range keys {
			max, lock, lockMax := policy.IPMaxFailures, policy.IPLock, policy.IPLock
			if k.Kind == "account" {
				max, lock, lockMax = policy.AccountMaxFailures, policy.AccountLock, policy.AccountLockMax
			}
			var failures, lockCount int
			var windowStart time.Time
			err := tx.QueryRow(ctx, `SELECT failures, window_started_at, lock_count FROM admin_login_throttle
				WHERE key_kind = $1 AND key = $2 FOR UPDATE`, k.Kind, k.Key).Scan(&failures, &windowStart, &lockCount)
			if errors.Is(err, pgx.ErrNoRows) {
				failures, windowStart, lockCount = 0, now, 0
			} else if err != nil {
				return err
			}
			if now.Sub(windowStart) >= policy.ThrottleWindow {
				failures, windowStart = 0, now
			}
			failures++
			var lockedUntil *time.Time
			if failures >= max {
				d := lock
				for i := 0; i < lockCount && d < lockMax; i++ {
					d *= 2
				}
				if d > lockMax {
					d = lockMax
				}
				until := now.Add(d)
				lockedUntil = &until
				lockCount++
				failures, windowStart = 0, now
			}
			if _, err := tx.Exec(ctx, `INSERT INTO admin_login_throttle (key_kind, key, failures, window_started_at, locked_until, lock_count, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (key_kind, key) DO UPDATE SET failures = EXCLUDED.failures, window_started_at = EXCLUDED.window_started_at,
				  locked_until = COALESCE(EXCLUDED.locked_until, admin_login_throttle.locked_until), lock_count = EXCLUDED.lock_count,
				  updated_at = EXCLUDED.updated_at`,
				k.Kind, k.Key, failures, windowStart, lockedUntil, lockCount, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (h HumanAuth) ClearThrottle(ctx context.Context, key humanauth.ThrottleKey) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM admin_login_throttle WHERE key_kind = $1 AND key = $2`, key.Kind, key.Key)
		return err
	})
}

func insertSession(ctx context.Context, tx pgx.Tx, s humanauth.NewSession) error {
	_, err := tx.Exec(ctx, `INSERT INTO admin_sessions (id, token_hash, csrf_hash, user_id, stage, security_version,
		created_at, last_seen_at, absolute_expires_at, auth_time, mfa_verified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9, $10)`,
		s.ID, s.TokenHash, s.CSRFHash, s.UserID, string(s.Stage), s.SecurityVersion, s.CreatedAt, s.AbsoluteExpiresAt, s.AuthTime, s.MFAVerifiedAt)
	return err
}

func (h HumanAuth) CreateSession(ctx context.Context, s humanauth.NewSession) error {
	return h.inTx(ctx, func(tx pgx.Tx) error { return insertSession(ctx, tx, s) })
}

// RotateSession revokes the old session and inserts its successor in one
// transaction; it fails if the old session was already revoked, so one
// challenge can never mint two successors.
func (h HumanAuth) RotateSession(ctx context.Context, oldID, reason string, s humanauth.NewSession, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at = $2, revoke_reason = $3 WHERE id = $1 AND revoked_at IS NULL`, oldID, now, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return humanauth.ErrUnauthenticated
		}
		return insertSession(ctx, tx, s)
	})
}

func (h HumanAuth) SessionByTokenHash(ctx context.Context, hash []byte) (humanauth.SessionRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	var rec humanauth.SessionRecord
	var stage string
	row := h.pool.QueryRow(ctx, `SELECT s.id::text, s.user_id::text, s.stage, s.security_version, s.csrf_hash, s.created_at, s.last_seen_at,
		s.absolute_expires_at, s.auth_time, s.mfa_verified_at, s.revoked_at IS NOT NULL
		FROM admin_sessions s WHERE s.token_hash = $1`, hash)
	if err := row.Scan(&rec.ID, &rec.UserID, &stage, &rec.SecurityVersion, &rec.CSRFHash, &rec.CreatedAt, &rec.LastSeenAt,
		&rec.AbsoluteExpiresAt, &rec.AuthTime, &rec.MFAVerifiedAt, &rec.Revoked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rec, humanauth.ErrUnauthenticated
		}
		return rec, err
	}
	rec.Stage = humanauth.Stage(stage)
	user, err := h.UserByID(ctx, rec.UserID)
	if err != nil {
		return rec, err
	}
	rec.User = user
	return rec, nil
}

func (h HumanAuth) TouchSession(ctx context.Context, id string, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE admin_sessions SET last_seen_at = $2 WHERE id = $1 AND last_seen_at < $2 AND revoked_at IS NULL`, id, now)
		return err
	})
}

func (h HumanAuth) RevokeSession(ctx context.Context, id, reason string, now time.Time) (bool, error) {
	var n int64
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at = $2, revoke_reason = $3 WHERE id = $1 AND revoked_at IS NULL`, id, now, reason)
		n = tag.RowsAffected()
		return err
	})
	return n == 1, err
}

func (h HumanAuth) RevokeUserSessions(ctx context.Context, userID, exceptID, reason string, now time.Time) (int, error) {
	var n int64
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at = $3, revoke_reason = $4
			WHERE user_id = $1 AND revoked_at IS NULL AND ($2 = '' OR id::text <> $2)`, userID, exceptID, now, reason)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}

func (h HumanAuth) GetMFA(ctx context.Context, userID string) (humanauth.MFARecord, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	var rec humanauth.MFARecord
	var step int64
	err := h.pool.QueryRow(ctx, `SELECT secret_ciphertext, key_version, enabled_at IS NOT NULL, last_used_step FROM admin_mfa_credentials WHERE user_id = $1`,
		userID).Scan(&rec.Ciphertext, &rec.KeyVersion, &rec.Enabled, &step)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, humanauth.ErrNotFound
	}
	rec.LastUsedStep = uint64(step)
	return rec, err
}

// PutPendingMFA stores (or replaces) a NOT-yet-enabled secret; an enabled
// credential is never overwritten here.
func (h HumanAuth) PutPendingMFA(ctx context.Context, userID string, ciphertext []byte, keyVersion int, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO admin_mfa_credentials (user_id, secret_ciphertext, key_version, created_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id) DO UPDATE SET secret_ciphertext = EXCLUDED.secret_ciphertext, key_version = EXCLUDED.key_version,
			  created_at = EXCLUDED.created_at, last_used_step = 0
			WHERE admin_mfa_credentials.enabled_at IS NULL`, userID, ciphertext, keyVersion, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return humanauth.ErrMFAAlreadyEnabled
		}
		return nil
	})
}

// EnableMFA enables the pending credential, replaces recovery codes,
// activates a PENDING_SETUP account and bumps the security version. The
// credential comparison fences activation to the secret actually verified,
// including replacement while confirmation was in progress.
func (h HumanAuth) EnableMFA(ctx context.Context, userID string, expectedCiphertext []byte, expectedKeyVersion int, step uint64, codeHashes [][]byte, now time.Time) (int64, error) {
	var version int64
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admin_mfa_credentials SET enabled_at = $2, last_used_step = $3
			WHERE user_id = $1 AND enabled_at IS NULL
			  AND secret_ciphertext = $4 AND key_version = $5`, userID, now, int64(step), expectedCiphertext, expectedKeyVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return humanauth.ErrConflict
		}
		if err := replaceCodes(ctx, tx, userID, codeHashes, now); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE admin_users SET status = CASE WHEN status = 'PENDING_SETUP' THEN 'ACTIVE' ELSE status END,
			security_version = security_version + 1, updated_at = $2 WHERE id = $1 RETURNING security_version`, userID, now).Scan(&version)
	})
	return version, err
}

// ConsumeTOTPStep records a used step; a step not newer than the last
// accepted one is a replay and loses (single winner under concurrency).
func (h HumanAuth) ConsumeTOTPStep(ctx context.Context, userID string, step uint64) (bool, error) {
	var n int64
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admin_mfa_credentials SET last_used_step = $2
			WHERE user_id = $1 AND enabled_at IS NOT NULL AND last_used_step < $2`, userID, int64(step))
		n = tag.RowsAffected()
		return err
	})
	return n == 1, err
}

// ConsumeRecoveryCode spends one unused code (single winner).
func (h HumanAuth) ConsumeRecoveryCode(ctx context.Context, userID string, codeHash []byte, now time.Time) (bool, error) {
	var n int64
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admin_recovery_codes SET used_at = $3 WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
			userID, codeHash, now)
		n = tag.RowsAffected()
		return err
	})
	return n == 1, err
}

func replaceCodes(ctx context.Context, tx pgx.Tx, userID string, codeHashes [][]byte, now time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, hash := range codeHashes {
		if _, err := tx.Exec(ctx, `INSERT INTO admin_recovery_codes (user_id, code_hash, created_at) VALUES ($1, $2, $3)`, userID, hash, now); err != nil {
			return err
		}
	}
	return nil
}

func (h HumanAuth) ReplaceRecoveryCodes(ctx context.Context, userID string, codeHashes [][]byte, now time.Time) error {
	return h.inTx(ctx, func(tx pgx.Tx) error { return replaceCodes(ctx, tx, userID, codeHashes, now) })
}

func (h HumanAuth) RemainingRecoveryCodes(ctx context.Context, userID string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	var n int
	err := h.pool.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

// ResetMFA deletes the credential and codes and bumps the security
// version (every session of the user stops working).
func (h HumanAuth) ResetMFA(ctx context.Context, userID string, now time.Time) (int64, error) {
	var version int64
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM admin_mfa_credentials WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE user_id = $1`, userID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE admin_users SET security_version = security_version + 1, updated_at = $2 WHERE id = $1 RETURNING security_version`,
			userID, now).Scan(&version)
	})
	return version, err
}

func nullUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (h HumanAuth) Audit(ctx context.Context, e humanauth.AuditEvent) error {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return h.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO auth_audit_events (occurred_at, actor_user_id, target_user_id, action, outcome, reason, client_ip, details)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			e.OccurredAt, nullUUID(e.ActorUserID), nullUUID(e.TargetUserID), e.Action, e.Outcome, nullText(e.Reason), nullText(e.ClientIP), raw)
		return err
	})
}

func (h HumanAuth) ListAudit(ctx context.Context, beforeID int64, limit int) ([]humanauth.AuditRow, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	if beforeID <= 0 {
		beforeID = 1<<62 - 1
	}
	rows, err := h.pool.Query(ctx, `SELECT id, occurred_at, COALESCE(actor_user_id::text, ''), COALESCE(target_user_id::text, ''),
		action, outcome, COALESCE(reason, ''), COALESCE(client_ip, ''), details
		FROM auth_audit_events WHERE id < $1 ORDER BY id DESC LIMIT $2`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []humanauth.AuditRow
	for rows.Next() {
		var r humanauth.AuditRow
		var raw []byte
		if err := rows.Scan(&r.ID, &r.OccurredAt, &r.ActorUserID, &r.TargetUserID, &r.Action, &r.Outcome, &r.Reason, &r.ClientIP, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &r.Details)
		out = append(out, r)
	}
	return out, rows.Err()
}
