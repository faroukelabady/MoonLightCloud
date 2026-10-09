package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pause after the service verifies the code but before the real PostgreSQL
// activation transaction. No sleeps or fake persistence are involved.
type enrollmentBarrier struct {
	humanauth.Store
	entered chan struct{}
	resume  <-chan struct{}
}

func (b enrollmentBarrier) EnableMFA(ctx context.Context, userID string, ciphertext []byte, keyVersion int, step uint64, hashes [][]byte, now time.Time) (int64, error) {
	select {
	case b.entered <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case <-b.resume:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return b.Store.EnableMFA(ctx, userID, ciphertext, keyVersion, step, hashes, now)
}

type enrollmentFixture struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	repo  HumanAuth
	svc   *humanauth.Service
	auth  humanauth.Authenticated
	token string
	now   time.Time
}

func newEnrollmentFixture(t *testing.T, wrap func(humanauth.Store) humanauth.Store) enrollmentFixture {
	t.Helper()
	pool, _ := openTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := humanauth.NewSecretBox(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewHumanAuth(pool, 5*time.Second)
	var store humanauth.Store = repo
	if wrap != nil {
		store = wrap(store)
	}
	now := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	svc := humanauth.NewService(store, box, key, clock.Fixed{T: now}, ids.System{}, humanauth.DefaultPolicy(), nil).
		WithArgon2(humanauth.Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16})
	const login, password = "enrollment@test.example", "synthetic-enrollment-password"
	if _, err := svc.BootstrapOwner(ctx, login, "Enrollment test", password, nil); err != nil {
		t.Fatal(err)
	}
	primary, err := svc.Login(ctx, login, password, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := svc.Authenticate(ctx, primary.Session.Token)
	if err != nil {
		t.Fatal(err)
	}
	return enrollmentFixture{ctx, pool, repo, svc, auth, primary.Session.Token, now}
}

func enrollmentCode(t *testing.T, e humanauth.Enrollment, now time.Time) string {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return humanauth.HOTP(secret, humanauth.TOTPStep(now))
}

func (f enrollmentFixture) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, query, f.auth.Session.UserID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f enrollmentFixture) assertPending(t *testing.T, expected humanauth.MFARecord) {
	t.Helper()
	current, err := f.repo.GetMFA(f.ctx, f.auth.Session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Enabled || current.LastUsedStep != 0 || current.KeyVersion != expected.KeyVersion || !bytes.Equal(current.Ciphertext, expected.Ciphertext) {
		t.Fatal("stale confirmation changed the pending credential")
	}
	user, err := f.repo.UserByID(f.ctx, f.auth.Session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.SecurityVersion != 1 || user.MFAEnabled {
		t.Fatal("stale confirmation activated MFA or bumped security version")
	}
	for _, query := range []string{
		`SELECT count(*) FROM admin_recovery_codes WHERE user_id = $1`,
		`SELECT count(*) FROM admin_sessions WHERE user_id = $1 AND stage = 'FULL'`,
		`SELECT count(*) FROM auth_audit_events WHERE target_user_id = $1 AND action = 'auth.mfa.enrolled'`,
	} {
		if f.count(t, query) != 0 {
			t.Fatal("stale confirmation produced recovery codes, a full session or success audit")
		}
	}
	if _, err := f.svc.Authenticate(f.ctx, f.token); err != nil {
		t.Fatal("stale confirmation revoked the setup session")
	}
}

type enrollmentOutcome struct {
	session humanauth.IssuedSession
	codes   []string
	err     error
}

func TestMFAEnrollmentReplacementRejectsStaleConfirmation(t *testing.T) {
	entered, resume := make(chan struct{}, 2), make(chan struct{})
	f := newEnrollmentFixture(t, func(s humanauth.Store) humanauth.Store { return enrollmentBarrier{s, entered, resume} })
	first, err := f.svc.StartEnrollment(f.ctx, f.auth)
	if err != nil {
		t.Fatal(err)
	}
	code := enrollmentCode(t, first, f.now)
	done := make(chan enrollmentOutcome, 1)
	go func() {
		s, codes, err := f.svc.ConfirmEnrollment(f.ctx, f.auth, code, "127.0.0.1")
		done <- enrollmentOutcome{s, codes, err}
	}()
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal("confirmation did not reach activation barrier")
	}
	second, err := f.svc.StartEnrollment(f.ctx, f.auth)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.repo.GetMFA(f.ctx, f.auth.Session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	close(resume)
	out := <-done
	if !errors.Is(out.err, humanauth.ErrConflict) || out.session.Token != "" || len(out.codes) != 0 {
		t.Fatal("stale confirmation must conflict without returning session or recovery codes")
	}
	f.assertPending(t, pending)
	full, codes, err := f.svc.ConfirmEnrollment(f.ctx, f.auth, enrollmentCode(t, second, f.now), "127.0.0.1")
	if err != nil || full.Stage != humanauth.StageFull || len(codes) != humanauth.RecoveryCodeCount {
		t.Fatalf("latest credential confirmation failed: %v", err)
	}
	if _, err := f.svc.Authenticate(f.ctx, full.Token); err != nil {
		t.Fatal("latest credential did not issue an accepted full session")
	}
	if _, err := f.svc.Authenticate(f.ctx, f.token); !errors.Is(err, humanauth.ErrUnauthenticated) {
		t.Fatal("successful enrollment must invalidate the old setup session")
	}
	current, err := f.repo.GetMFA(f.ctx, f.auth.Session.UserID)
	if err != nil || !current.Enabled || current.LastUsedStep != humanauth.TOTPStep(f.now) || !bytes.Equal(current.Ciphertext, pending.Ciphertext) {
		t.Fatal("successful enrollment did not enable exactly the latest verified credential/step")
	}
}

func TestMFAEnrollmentCredentialFence(t *testing.T) {
	for _, change := range []string{"ciphertext", "key version"} {
		t.Run(change, func(t *testing.T) {
			f := newEnrollmentFixture(t, nil)
			if _, err := f.svc.StartEnrollment(f.ctx, f.auth); err != nil {
				t.Fatal(err)
			}
			pending, err := f.repo.GetMFA(f.ctx, f.auth.Session.UserID)
			if err != nil {
				t.Fatal(err)
			}
			expected := pending
			if change == "ciphertext" {
				expected.Ciphertext = append([]byte(nil), pending.Ciphertext...)
				expected.Ciphertext[0] ^= 1
			} else {
				expected.KeyVersion++
			}
			_, hashes, err := humanauth.NewRecoveryCodes()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.repo.EnableMFA(f.ctx, f.auth.Session.UserID, expected.Ciphertext, expected.KeyVersion, humanauth.TOTPStep(f.now), hashes, f.now); !errors.Is(err, humanauth.ErrConflict) {
				t.Fatal("credential mismatch must conflict")
			}
			f.assertPending(t, pending)
		})
	}
}

func TestMFAEnrollmentConcurrentConfirmationsSingleWinner(t *testing.T) {
	entered, resume := make(chan struct{}, 2), make(chan struct{})
	f := newEnrollmentFixture(t, func(s humanauth.Store) humanauth.Store { return enrollmentBarrier{s, entered, resume} })
	enrollment, err := f.svc.StartEnrollment(f.ctx, f.auth)
	if err != nil {
		t.Fatal(err)
	}
	code := enrollmentCode(t, enrollment, f.now)
	done := make(chan enrollmentOutcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			s, codes, err := f.svc.ConfirmEnrollment(f.ctx, f.auth, code, "127.0.0.1")
			done <- enrollmentOutcome{s, codes, err}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-f.ctx.Done():
			t.Fatal("confirmations did not both verify before activation")
		}
	}
	close(resume)
	wins := 0
	for i := 0; i < 2; i++ {
		out := <-done
		if out.err == nil {
			wins++
			if len(out.codes) != humanauth.RecoveryCodeCount {
				t.Fatal("winner did not receive recovery codes")
			}
			if _, err := f.svc.Authenticate(f.ctx, out.session.Token); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(out.err, humanauth.ErrConflict) || out.session.Token != "" || len(out.codes) != 0 {
			t.Fatal("loser must conflict without session or codes")
		}
	}
	user, err := f.repo.UserByID(f.ctx, f.auth.Session.UserID)
	if err != nil || wins != 1 || user.SecurityVersion != 2 || !user.MFAEnabled {
		t.Fatal("concurrent enrollment must activate once and bump security version once")
	}
	if f.count(t, `SELECT count(*) FROM admin_recovery_codes WHERE user_id = $1`) != humanauth.RecoveryCodeCount ||
		f.count(t, `SELECT count(*) FROM admin_sessions WHERE user_id = $1 AND stage = 'FULL'`) != 1 ||
		f.count(t, `SELECT count(*) FROM auth_audit_events WHERE target_user_id = $1 AND action = 'auth.mfa.enrolled'`) != 1 {
		t.Fatal("concurrent enrollment duplicated durable side effects")
	}
}
