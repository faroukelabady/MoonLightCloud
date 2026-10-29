package catalogadmin

// Shop permissions (Retail ADR-037, 2026-10-29). The shop decides in Retail
// what Cloud may change and Retail advertises the allowed scopes next to
// CapabilityV1. Retail rejects any command outside them, so Cloud's use is
// advisory: it refuses to queue what no Store device allows and asks for a
// fresh MFA code before queuing product, price or catalog changes.

// Scope groups command types under one shop permission.
type Scope string

const (
	ScopeOnlineSelling Scope = "online_selling"
	ScopeProducts      Scope = "products"
	ScopeCatalog       Scope = "catalog"
)

// Scope capabilities Retail advertises when the shop allows the scope.
const (
	CapabilityScopeOnlineSelling = "catalog_admin_scope_online_selling_v1"
	CapabilityScopeProducts      = "catalog_admin_scope_products_v1"
	CapabilityScopeCatalog       = "catalog_admin_scope_catalog_v1"
)

// Result and refusal codes for shop permissions.
const (
	// CodePermissionDenied is Retail's terminal rejection of a command
	// outside the shop's permissions.
	CodePermissionDenied = "CLOUD_PERMISSION_DENIED"
	// CodePermissionDisabled refuses to queue a command no device of the
	// Store currently allows.
	CodePermissionDisabled = "CLOUD_PERMISSION_DISABLED"
	// CodeMFARequired and CodeMFAInvalid refuse a product, price or
	// catalog change without a valid fresh authenticator code.
	CodeMFARequired = "MFA_REQUIRED"
	CodeMFAInvalid  = "MFA_INVALID"
)

// KnownCapabilities are the capability rows Cloud records.
func KnownCapabilities() []string {
	return []string{CapabilityV1, CapabilityScopeOnlineSelling, CapabilityScopeProducts, CapabilityScopeCatalog}
}

// ScopeOf maps a command type to its shop permission ("" when unknown).
// It mirrors Retail's admincommand.ScopeOf.
func ScopeOf(commandType string) Scope {
	switch commandType {
	case TypeProductOnlinePolicyUpdateV1, TypeCategoryOnlinePolicyUpdateV1:
		return ScopeOnlineSelling
	case TypeProductDetailsUpdateV1, TypeProductVariantsUpdateV1,
		TypeProductVariantUpdateV1, TypeVariantAttributesUpdateV1:
		return ScopeProducts
	case TypeProductClassificationUpdateV1, TypeCategoryDetailsUpdateV1,
		TypeCategoryParentsUpdateV1, TypeTagDetailsUpdateV1,
		TypeProductTypeCreateV1, TypeProductTypeDetailsUpdateV1,
		TypeProductTypeDimensionsUpdateV1, TypeProductTypeCapabilitiesUpdateV1,
		TypeProductTypeStatusUpdateV1, TypeProductTypeAssignV1:
		return ScopeCatalog
	}
	return ""
}

// CapabilityOf is the capability that advertises a scope.
func CapabilityOf(scope Scope) string {
	switch scope {
	case ScopeOnlineSelling:
		return CapabilityScopeOnlineSelling
	case ScopeProducts:
		return CapabilityScopeProducts
	case ScopeCatalog:
		return CapabilityScopeCatalog
	}
	return ""
}

// RequiresMFA reports whether queuing this command needs a fresh
// authenticator code: product, price and catalog changes do; switching
// online selling does not.
func RequiresMFA(commandType string) bool {
	scope := ScopeOf(commandType)
	return scope == ScopeProducts || scope == ScopeCatalog
}

// StorePermissions is what the Store's devices currently allow. A scope
// is allowed when at least one active, bound, command-capable device
// advertises it.
type StorePermissions struct {
	OnlineSelling bool `json:"online_selling"`
	Products      bool `json:"products"`
	Catalog       bool `json:"catalog"`
	// Devices counts the active bound devices that can apply commands.
	Devices int `json:"devices"`
}

// Allows reports whether the scope of commandType is allowed.
func (p StorePermissions) Allows(commandType string) bool {
	switch ScopeOf(commandType) {
	case ScopeOnlineSelling:
		return p.OnlineSelling
	case ScopeProducts:
		return p.Products
	case ScopeCatalog:
		return p.Catalog
	}
	return false
}
