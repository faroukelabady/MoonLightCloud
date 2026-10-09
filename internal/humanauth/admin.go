package humanauth

import (
	"context"
	"sort"
)

// NewUserRequest creates an account in PENDING_SETUP.
type NewUserRequest struct {
	Login       string
	DisplayName string
	Role        Role
	AllStores   bool
	Stores      []string
}

// CreatedUser carries the one-time activation token, shown exactly once.
type CreatedUser struct {
	User            User
	ActivationToken string
}

// requireManage checks users.manage and the actor's reach over a Store
// set: an actor restricted to some Stores can only manage users inside
// those Stores and can never grant all-Stores access.
func requireManage(actor Principal, allStores bool, stores []string) error {
	if !actor.Can(PermUsersManage) {
		return ErrForbidden
	}
	if actor.AllStores {
		return nil
	}
	if allStores {
		return ErrForbidden
	}
	for _, id := range stores {
		if !actor.Stores[id] {
			return ErrForbidden
		}
	}
	return nil
}

// requireReach checks the actor may manage an existing target account.
func requireReach(actor Principal, target User) error {
	return requireManage(actor, target.AllStores, target.Stores)
}

func uniqueStores(stores []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(stores))
	for _, id := range stores {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// CreateUser provisions a PENDING_SETUP account and its one-time
// activation token (OWNER only; no public registration exists).
func (s *Service) CreateUser(ctx context.Context, actor Principal, req NewUserRequest) (CreatedUser, error) {
	login, err := NormalizeLogin(req.Login)
	if err != nil {
		return CreatedUser{}, err
	}
	if !ValidDisplayName(req.DisplayName) || !req.Role.Valid() {
		return CreatedUser{}, ErrInvalidInput
	}
	stores := uniqueStores(req.Stores)
	if !req.AllStores && len(stores) == 0 {
		return CreatedUser{}, ErrInvalidInput
	}
	if req.AllStores {
		stores = nil
	}
	if err := requireManage(actor, req.AllStores, stores); err != nil {
		s.denied(ctx, actor, "", "user.create")
		return CreatedUser{}, err
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return CreatedUser{}, err
	}
	now := s.now()
	user := User{ID: s.ids.New(), Login: login, DisplayName: req.DisplayName, Role: req.Role, Status: StatusPendingSetup,
		AllStores: req.AllStores, Stores: stores, SecurityVersion: 1}
	if err := s.store.CreateUser(ctx, user, actor.UserID, tokenHash, now.Add(s.policy.ActivationTTL), now); err != nil {
		return CreatedUser{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: actor.UserID, TargetUserID: user.ID, Action: "user.created", Outcome: "success",
		Details: map[string]any{"role": string(req.Role), "all_stores": req.AllStores, "stores": stores}})
	created, err := s.store.UserByID(ctx, user.ID)
	if err != nil {
		return CreatedUser{}, err
	}
	return CreatedUser{User: created, ActivationToken: token}, nil
}

// ReissueActivation replaces a PENDING_SETUP user's activation token.
func (s *Service) ReissueActivation(ctx context.Context, actor Principal, userID string) (string, error) {
	target, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if err := requireReach(actor, target); err != nil {
		s.denied(ctx, actor, userID, "user.activation_issued")
		return "", err
	}
	if target.Status != StatusPendingSetup {
		return "", ErrConflict
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	if err := s.store.IssueActivation(ctx, userID, actor.UserID, tokenHash, now.Add(s.policy.ActivationTTL), now); err != nil {
		return "", err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: actor.UserID, TargetUserID: userID, Action: "user.activation_issued", Outcome: "success"})
	return token, nil
}

// ChangeRole changes a user's role. Granting or removing OWNER requires
// users.manage (OWNER only); the last active OWNER can never be demoted.
func (s *Service) ChangeRole(ctx context.Context, actor Principal, userID string, role Role) (User, error) {
	if !role.Valid() {
		return User{}, ErrInvalidInput
	}
	target, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if err := requireReach(actor, target); err != nil {
		s.denied(ctx, actor, userID, "user.role_changed")
		return User{}, err
	}
	// Only an all-Stores OWNER may mint a new OWNER.
	if role == RoleOwner && target.Role != RoleOwner && !actor.AllStores {
		s.denied(ctx, actor, userID, "user.role_changed")
		return User{}, ErrForbidden
	}
	now := s.now()
	updated, err := s.store.ChangeUser(ctx, userID, UserChange{Role: &role, BumpSecurity: true}, now)
	if err != nil {
		return User{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: actor.UserID, TargetUserID: userID, Action: "user.role_changed", Outcome: "success",
		Details: map[string]any{"from": string(target.Role), "to": string(role)}})
	return updated, nil
}

// SetStatus enables or disables an account. Disabling invalidates every
// session immediately; the last active OWNER can never be disabled.
func (s *Service) SetStatus(ctx context.Context, actor Principal, userID string, status Status) (User, error) {
	if status != StatusActive && status != StatusDisabled {
		return User{}, ErrInvalidInput
	}
	target, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if err := requireReach(actor, target); err != nil {
		s.denied(ctx, actor, userID, "user.status_changed")
		return User{}, err
	}
	// Re-enabling restores ACTIVE only for accounts that completed setup.
	if status == StatusActive && (target.PasswordHash == "" || !target.MFAEnabled) {
		return User{}, ErrConflict
	}
	now := s.now()
	updated, err := s.store.ChangeUser(ctx, userID, UserChange{Status: &status, BumpSecurity: true}, now)
	if err != nil {
		return User{}, err
	}
	if status == StatusDisabled {
		if _, err := s.store.RevokeUserSessions(ctx, userID, "", "account_disabled", now); err != nil {
			return User{}, err
		}
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: actor.UserID, TargetUserID: userID, Action: "user.status_changed", Outcome: "success",
		Details: map[string]any{"from": string(target.Status), "to": string(status)}})
	return updated, nil
}

// SetMemberships replaces a user's Store access. Effective immediately on
// the target's next request (authorization reads memberships live).
func (s *Service) SetMemberships(ctx context.Context, actor Principal, userID string, allStores bool, stores []string) (User, error) {
	stores = uniqueStores(stores)
	if !allStores && len(stores) == 0 {
		return User{}, ErrInvalidInput
	}
	if allStores {
		stores = []string{}
	}
	target, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if err := requireReach(actor, target); err != nil {
		s.denied(ctx, actor, userID, "user.memberships_changed")
		return User{}, err
	}
	if err := requireManage(actor, allStores, stores); err != nil {
		s.denied(ctx, actor, userID, "user.memberships_changed")
		return User{}, err
	}
	now := s.now()
	updated, err := s.store.ChangeUser(ctx, userID, UserChange{AllStores: &allStores, Stores: &stores}, now)
	if err != nil {
		return User{}, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: actor.UserID, TargetUserID: userID, Action: "user.memberships_changed", Outcome: "success",
		Details: map[string]any{"all_stores": allStores, "stores": stores}})
	return updated, nil
}

// ResetUserMFA clears another user's MFA (security.manage); the user must
// re-enroll at next login and every session is invalidated.
func (s *Service) ResetUserMFA(ctx context.Context, actor Principal, userID string) error {
	if !actor.Can(PermSecurityManage) {
		s.denied(ctx, actor, userID, "auth.mfa.reset")
		return ErrForbidden
	}
	target, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := requireReach(actor, target); err != nil {
		s.denied(ctx, actor, userID, "auth.mfa.reset")
		return err
	}
	now := s.now()
	if _, err := s.store.ResetMFA(ctx, userID, now); err != nil {
		return err
	}
	if _, err := s.store.RevokeUserSessions(ctx, userID, "", "mfa_reset", now); err != nil {
		return err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: actor.UserID, TargetUserID: userID, Action: "auth.mfa.reset", Outcome: "success"})
	return nil
}

// RevokeSessions revokes every session of a user (users.manage) or the
// actor's own other sessions.
func (s *Service) RevokeSessions(ctx context.Context, actor Principal, userID string) (int, error) {
	if userID != actor.UserID {
		target, err := s.store.UserByID(ctx, userID)
		if err != nil {
			return 0, err
		}
		if err := requireReach(actor, target); err != nil {
			s.denied(ctx, actor, userID, "session.revoked")
			return 0, err
		}
	}
	except := ""
	if userID == actor.UserID {
		except = actor.SessionID
	}
	now := s.now()
	n, err := s.store.RevokeUserSessions(ctx, userID, except, "admin_revoked", now)
	if err != nil {
		return 0, err
	}
	s.audit(ctx, AuditEvent{OccurredAt: now, ActorUserID: actor.UserID, TargetUserID: userID, Action: "session.revoked", Outcome: "success",
		Details: map[string]any{"count": n}})
	return n, nil
}

// ListUsers pages accounts by login (bounded; users.read).
func (s *Service) ListUsers(ctx context.Context, actor Principal, afterLogin string, limit int) ([]User, string, error) {
	if !actor.Can(PermUsersRead) {
		return nil, "", ErrForbidden
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.store.ListUsers(ctx, afterLogin, limit+1)
	if err != nil {
		return nil, "", err
	}
	visible := make([]User, 0, len(rows))
	for _, u := range rows {
		if requireReach(actor, u) == nil || u.ID == actor.UserID {
			visible = append(visible, u)
		}
	}
	next := ""
	if len(rows) > limit {
		next = rows[limit-1].Login
		if len(visible) > limit {
			visible = visible[:limit]
		}
	}
	return visible, next, nil
}

// ListAudit pages the security audit newest first (bounded; users.read on
// an all-Stores account: the audit spans every Store).
func (s *Service) ListAudit(ctx context.Context, actor Principal, beforeID int64, limit int) ([]AuditRow, int64, error) {
	if !actor.Can(PermUsersRead) || !actor.AllStores {
		return nil, 0, ErrForbidden
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.store.ListAudit(ctx, beforeID, limit+1)
	if err != nil {
		return nil, 0, err
	}
	var next int64
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID
	}
	return rows, next, nil
}

func (s *Service) denied(ctx context.Context, actor Principal, targetID, action string) {
	s.audit(ctx, AuditEvent{ActorUserID: actor.UserID, TargetUserID: targetID, Action: action, Outcome: "denied", Reason: "FORBIDDEN"})
}
