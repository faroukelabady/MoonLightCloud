// Package humanauth is the Cloud human identity, authentication and
// authorization core (ADR-0053): explicitly provisioned OWNER/ADMIN
// accounts, Argon2id passwords, TOTP MFA with encrypted secrets and hashed
// one-time recovery codes, opaque server-side sessions, bounded login
// throttling, named permissions and explicit Store memberships.
//
// Human identity is never a Store and never a Retail device. Device
// credentials authenticate only device routes (internal/auth); this package
// authenticates only humans.
package humanauth

import "sort"

// Role is a human account role.
type Role string

const (
	RoleOwner Role = "OWNER"
	RoleAdmin Role = "ADMIN"
)

// Valid reports a known role.
func (r Role) Valid() bool { return r == RoleOwner || r == RoleAdmin }

// Status is an account lifecycle state.
type Status string

const (
	StatusPendingSetup Status = "PENDING_SETUP"
	StatusActive       Status = "ACTIVE"
	StatusDisabled     Status = "DISABLED"
)

// Permission is a named capability checked server-side on every route.
// Handlers never branch on role names.
type Permission string

const (
	PermCatalogRead     Permission = "catalog.read"
	PermCatalogManage   Permission = "catalog.manage"
	PermReportsRead     Permission = "reports.read"
	PermDevicesRead     Permission = "devices.read"
	PermDevicesManage   Permission = "devices.manage"
	PermOperationsRead  Permission = "operations.read"
	PermOperationsAct   Permission = "operations.manage"
	PermProvidersManage Permission = "providers.manage"
	PermReleasesRead    Permission = "releases.read"
	PermReleasesManage  Permission = "releases.manage"
	PermRolloutsRead    Permission = "rollouts.read"
	PermRolloutsManage  Permission = "rollouts.manage"
	PermUsersRead       Permission = "users.read"
	PermUsersManage     Permission = "users.manage"
	PermSecurityManage  Permission = "security.manage"
)

// adminPermissions operate the shop but never touch account ownership or
// security controls.
var adminPermissions = []Permission{
	PermCatalogRead, PermCatalogManage, PermReportsRead,
	PermDevicesRead, PermDevicesManage, PermOperationsRead, PermOperationsAct,
	PermProvidersManage, PermReleasesRead, PermReleasesManage,
	PermRolloutsRead, PermRolloutsManage,
}

// ownerOnly are reserved to OWNER: user administration and security.
var ownerOnly = []Permission{PermUsersRead, PermUsersManage, PermSecurityManage}

var rolePermissions = map[Role]map[Permission]bool{
	RoleOwner: toSet(append(append([]Permission{}, adminPermissions...), ownerOnly...)),
	RoleAdmin: toSet(adminPermissions),
}

func toSet(perms []Permission) map[Permission]bool {
	out := make(map[Permission]bool, len(perms))
	for _, p := range perms {
		out[p] = true
	}
	return out
}

// RoleHas is the single central role -> permission mapping.
func RoleHas(role Role, perm Permission) bool { return rolePermissions[role][perm] }

// PermissionsOf lists a role's permissions in stable order.
func PermissionsOf(role Role) []string {
	out := make([]string, 0, len(rolePermissions[role]))
	for p := range rolePermissions[role] {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}

// Principal is the authenticated human acting on one request. Store access
// and permissions are read from current server state per request, so a
// removed membership or demotion takes effect immediately.
type Principal struct {
	UserID      string
	Login       string
	DisplayName string
	Role        Role
	AllStores   bool
	Stores      map[string]bool
	SessionID   string
	AuthTime    int64 // unix seconds of the primary (password) authentication
	MFAVerified int64 // unix seconds of the MFA verification of this session
}

// Can reports a permission.
func (p Principal) Can(perm Permission) bool { return RoleHas(p.Role, perm) }

// CanAccessStore reports Store access: every Store for all-Stores users,
// otherwise only explicit memberships.
func (p Principal) CanAccessStore(storeID string) bool {
	if p.AllStores {
		return true
	}
	return storeID != "" && p.Stores[storeID]
}

// StoreIDs returns the explicit memberships in stable order.
func (p Principal) StoreIDs() []string {
	out := make([]string, 0, len(p.Stores))
	for id := range p.Stores {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
