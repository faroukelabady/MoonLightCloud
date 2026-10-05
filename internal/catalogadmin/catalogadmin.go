// Package catalogadmin implements Phase 16 Cloud-admin catalog
// control: typed immutable Store-scoped commands that Retail pulls
// over authenticated outbound device control and applies through
// canonical Retail services.
//
// Cloud authority: operator intent + durable commands. Retail
// authority: catalog business mutation. Cloud projections are never
// written from this package: convergence is observed when normal
// Retail sync events project the resulting revision back.
package catalogadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Capability Retail advertises when it can apply Phase 16 commands.
// A missing capability row means incapable (old Retail): Cloud never
// delivers unknown commands there.
const CapabilityV1 = "catalog_admin_commands_v1"

// Versioned command types. No generic catalog.patch exists.
const (
	TypeProductDetailsUpdateV1        = "catalog.product.details.update.v1"
	TypeProductOnlinePolicyUpdateV1   = "catalog.product.online-policy.update.v1"
	TypeProductClassificationUpdateV1 = "catalog.product.classification.update.v1"
	TypeCategoryDetailsUpdateV1       = "catalog.category.details.update.v1"
	TypeCategoryParentsUpdateV1       = "catalog.category.parents.update.v1"
	TypeCategoryOnlinePolicyUpdateV1  = "catalog.category.online-policy.update.v1"
	TypeTagDetailsUpdateV1            = "catalog.tag.details.update.v1"
	TypeProductConfigurationsUpdateV1 = "catalog.product.configurations.update.v1"
)

// KnownTypes lists every creatable command type.
func KnownTypes() []string {
	return []string{
		TypeProductDetailsUpdateV1,
		TypeProductOnlinePolicyUpdateV1,
		TypeProductClassificationUpdateV1,
		TypeCategoryDetailsUpdateV1,
		TypeCategoryParentsUpdateV1,
		TypeCategoryOnlinePolicyUpdateV1,
		TypeTagDetailsUpdateV1,
		TypeProductConfigurationsUpdateV1,
	}
}

// IsKnownType reports whether t is creatable.
func IsKnownType(t string) bool {
	for _, k := range KnownTypes() {
		if k == t {
			return true
		}
	}
	return false
}

// Machine result codes shared with Retail receipts.
const (
	CodeApplied            = "APPLIED"
	CodeConflict           = "REVISION_CONFLICT"
	CodeValidationFailed   = "VALIDATION_FAILED"
	CodeEntityNotFound     = "ENTITY_NOT_FOUND"
	CodeStoreScopeConflict = "STORE_SCOPE_CONFLICT"
	CodeUnsupportedCommand = "UNSUPPORTED_COMMAND"
	CodePayloadMismatch    = "COMMAND_ID_PAYLOAD_MISMATCH"
	CodeDependencyMissing  = "DEPENDENCY_MISSING"
	CodeRetryableFailure   = "RETRYABLE_FAILURE"
)

// Command statuses: PENDING/CANCELLED on the command row.
const (
	CommandPending   = "PENDING"
	CommandCancelled = "CANCELLED"
)

// Target statuses: one row per eligible device.
const (
	TargetPending           = "PENDING"
	TargetDelivered         = "DELIVERED"
	TargetApplied           = "APPLIED"
	TargetConflict          = "CONFLICT"
	TargetRejected          = "REJECTED"
	TargetBlockedCapability = "BLOCKED_CAPABILITY"
	TargetSkippedRevoked    = "SKIPPED_REVOKED"
	TargetCancelled         = "CANCELLED"
)

// Aggregate states derived for the dashboard. APPLIED means every
// target applied on Retail; CONVERGED additionally requires the Cloud
// projection to have reached the resulting revision. Creation is never
// reported as success.
const (
	AggregatePending           = "PENDING"
	AggregateDelivered         = "DELIVERED"
	AggregateApplied           = "APPLIED"
	AggregateConverged         = "CONVERGED"
	AggregatePartial           = "PARTIAL"
	AggregateConflict          = "CONFLICT"
	AggregateBlockedCapability = "BLOCKED_CAPABILITY"
	AggregateCancelled         = "CANCELLED"
)

// EntityKeyOf returns the payload entity-ID field for a type.
func EntityKeyOf(typ string) string {
	switch typ {
	case TypeProductDetailsUpdateV1, TypeProductOnlinePolicyUpdateV1,
		TypeProductClassificationUpdateV1, TypeProductConfigurationsUpdateV1:
		return "product_id"
	case TypeCategoryDetailsUpdateV1, TypeCategoryParentsUpdateV1,
		TypeCategoryOnlinePolicyUpdateV1:
		return "category_id"
	case TypeTagDetailsUpdateV1:
		return "tag_id"
	}
	return ""
}

// ExpectedRevisionKeyOf returns the expected-revision field for a type.
func ExpectedRevisionKeyOf(typ string) string {
	switch typ {
	case TypeProductOnlinePolicyUpdateV1:
		return "expected_sales_policy_revision"
	case TypeProductConfigurationsUpdateV1:
		return "expected_configuration_revision"
	default:
		return "expected_catalog_revision"
	}
}

// HashPayload returns deterministic hex SHA-256 over canonical JSON
// (same algorithm as Retail: unmarshal to a generic map, remarshal,
// hash — key order independent, byte-identical across repos).
func HashPayload(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("command payload must be a JSON object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("command payload must be a JSON object")
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateNewCommand checks type, Store, entity identity, expected
// revision presence and payload bounds without touching projections.
// Deep business validation stays Retail-side at apply time.
func ValidateNewCommand(typ, storeID, entityID string, expectedRevision int64, payload []byte) (map[string]any, error) {
	if !IsKnownType(typ) {
		return nil, fmt.Errorf("unknown command type %q", typ)
	}
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return nil, fmt.Errorf("invalid store_id: must be UUID")
	}
	if _, err := uuid.Parse(strings.TrimSpace(entityID)); err != nil {
		return nil, fmt.Errorf("invalid entity id: must be UUID")
	}
	if expectedRevision < 0 {
		return nil, fmt.Errorf("invalid expected revision")
	}
	if len(payload) == 0 || len(payload) > 64*1024 {
		return nil, fmt.Errorf("command payload must be 1..65536 bytes")
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("command payload must be a JSON object")
	}
	if err := boundPayload(typ, decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// boundPayload enforces collection bounds using existing domain
// limits: translations/descriptions, parent/tag ID lists, frame
// configuration sets. Money stays int64 minor units (never float
// authority); blank-vs-zero USD is preserved by the nullable field.
func boundPayload(typ string, payload map[string]any) error {
	strLen := func(key string, max int) error {
		raw, present := payload[key]
		if !present || raw == nil {
			return nil
		}
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("invalid %s", key)
		}
		if len([]rune(s)) > max {
			return fmt.Errorf("%s exceeds %d characters", key, max)
		}
		return nil
	}
	idList := func(key string, max int) error {
		raw, present := payload[key]
		if !present || raw == nil {
			return nil
		}
		list, ok := raw.([]any)
		if !ok || len(list) > max {
			return fmt.Errorf("invalid %s", key)
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("invalid %s entry", key)
			}
			if _, err := uuid.Parse(strings.TrimSpace(s)); err != nil {
				return fmt.Errorf("invalid %s entry", key)
			}
		}
		return nil
	}
	for _, key := range []string{"arabic_name", "english_name", "name_ar", "name_en"} {
		if err := strLen(key, 200); err != nil {
			return err
		}
	}
	for _, key := range []string{"arabic_description", "english_description"} {
		if err := strLen(key, 2000); err != nil {
			return err
		}
	}
	if err := idList("parent_ids", 16); err != nil {
		return err
	}
	if err := idList("subcategory_ids", 64); err != nil {
		return err
	}
	if err := idList("tag_ids", 64); err != nil {
		return err
	}
	if typ == TypeProductConfigurationsUpdateV1 {
		raw, present := payload["configurations"]
		if !present {
			return fmt.Errorf("missing configurations")
		}
		list, ok := raw.([]any)
		if !ok || len(list) > 100 {
			return fmt.Errorf("invalid configurations: at most 100")
		}
	}
	// Money travels as digit strings (never JSON numbers): float64
	// cannot represent values above 2^53, so the string-only rule is
	// enforced at creation as well as at Retail apply time. Null is
	// allowed only for explicitly nullable USD deltas.
	for _, key := range []string{"egp_price_cents", "usd_price_cents", "cost_cents", "egp_delta_cents"} {
		raw, present := payload[key]
		if !present || raw == nil {
			continue
		}
		if err := checkMinorString(key, raw); err != nil {
			return err
		}
	}
	if raw, present := payload["usd_delta_cents"]; present && raw != nil {
		if err := checkMinorString("usd_delta_cents", raw); err != nil {
			return err
		}
	}
	return nil
}

// checkMinorString enforces non-empty ASCII digits (int64 range is
// rechecked at Retail; creation only guards shape).
func checkMinorString(key string, raw any) error {
	s, ok := raw.(string)
	if !ok || s == "" {
		return fmt.Errorf("invalid %s: want minor-unit digit string", key)
	}
	if len(s) > 19 {
		return fmt.Errorf("invalid %s: exceeds int64 range", key)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return fmt.Errorf("invalid %s: want minor-unit digit string", key)
		}
	}
	return nil
}

// Aggregate derives the Store-level command state from per-target
// states. converged is ORed in by the caller only when the projection
// has reached the resulting revision; it is never time-based.
func Aggregate(commandStatus string, targets []string, converged bool) string {
	if commandStatus == CommandCancelled {
		return AggregateCancelled
	}
	if len(targets) == 0 {
		return AggregatePending
	}
	applied, conflict, rejected, blocked, pending, delivered := 0, 0, 0, 0, 0, 0
	for _, s := range targets {
		switch s {
		case TargetApplied:
			applied++
		case TargetConflict:
			conflict++
		case TargetRejected:
			rejected++
		case TargetBlockedCapability, TargetSkippedRevoked:
			blocked++
		case TargetDelivered:
			delivered++
		default:
			pending++
		}
	}
	switch {
	case applied > 0 && (conflict+rejected) > 0:
		return AggregatePartial
	case conflict > 0 && applied == 0 && pending == 0 && delivered == 0:
		return AggregateConflict
	case conflict > 0 || rejected > 0:
		return AggregatePartial
	case applied == len(targets):
		if converged {
			return AggregateConverged
		}
		return AggregateApplied
	case blocked > 0 && applied+delivered+pending == 0:
		return AggregateBlockedCapability
	case delivered > 0:
		return AggregateDelivered
	default:
		return AggregatePending
	}
}
