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
	"math"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
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
	// Phase 17 variant commands (SKU/inventory ownership on variant).
	TypeProductVariantsUpdateV1   = "catalog.product.variants.update.v1"
	TypeProductVariantUpdateV1    = "catalog.product.variant.update.v1"
	TypeVariantAttributesUpdateV1 = "catalog.variant.attributes.update.v1"
	// Phase 17-R2 ProductType commands (ADR-0050 §45/§46). Creation
	// mints canonical identity Retail-side (Cloud validates shape only);
	// all mutations are expected-revision fenced.
	TypeProductTypeCreateV1             = "catalog.product-type.create.v1"
	TypeProductTypeDetailsUpdateV1      = "catalog.product-type.details.update.v1"
	TypeProductTypeDimensionsUpdateV1   = "catalog.product-type.dimensions.update.v1"
	TypeProductTypeCapabilitiesUpdateV1 = "catalog.product-type.capabilities.update.v1"
	TypeProductTypeStatusUpdateV1       = "catalog.product-type.status.update.v1"
	TypeProductTypeAssignV1             = "catalog.product.type.assign.v1"
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
		TypeProductVariantsUpdateV1,
		TypeProductVariantUpdateV1,
		TypeVariantAttributesUpdateV1,
		TypeProductTypeCreateV1,
		TypeProductTypeDetailsUpdateV1,
		TypeProductTypeDimensionsUpdateV1,
		TypeProductTypeCapabilitiesUpdateV1,
		TypeProductTypeStatusUpdateV1,
		TypeProductTypeAssignV1,
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

// EntityKeyOf returns the payload entity-ID field for a type. Create
// commands address a not-yet-existing entity: they carry NO entity key
// ("" — the requested stable key travels as payload "code").
func EntityKeyOf(typ string) string {
	switch typ {
	case TypeProductDetailsUpdateV1, TypeProductOnlinePolicyUpdateV1,
		TypeProductClassificationUpdateV1, TypeProductConfigurationsUpdateV1,
		TypeProductVariantsUpdateV1:
		return "product_id"
	case TypeCategoryDetailsUpdateV1, TypeCategoryParentsUpdateV1,
		TypeCategoryOnlinePolicyUpdateV1:
		return "category_id"
	case TypeTagDetailsUpdateV1:
		return "tag_id"
	case TypeProductVariantUpdateV1, TypeVariantAttributesUpdateV1:
		return "variant_id"
	case TypeProductTypeCreateV1:
		return ""
	case TypeProductTypeDetailsUpdateV1,
		TypeProductTypeDimensionsUpdateV1, TypeProductTypeCapabilitiesUpdateV1,
		TypeProductTypeStatusUpdateV1:
		return "product_type_id"
	case TypeProductTypeAssignV1:
		return "product_id"
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
	case TypeProductVariantUpdateV1, TypeVariantAttributesUpdateV1:
		// Variant-scoped commands version on the variant catalog stream.
		return "expected_variant_revision"
	case TypeProductTypeCreateV1, TypeProductTypeDetailsUpdateV1,
		TypeProductTypeDimensionsUpdateV1, TypeProductTypeCapabilitiesUpdateV1,
		TypeProductTypeStatusUpdateV1:
		// ProductType commands version on the type stream.
		return "expected_type_revision"
	case TypeProductTypeAssignV1:
		// Assignment versions on the Product aggregate stream.
		return "expected_catalog_revision"
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
	if typ == TypeProductTypeCreateV1 {
		// Explicit absent marker (see below); validated there.
	} else if _, err := uuid.Parse(strings.TrimSpace(entityID)); err != nil {
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
	if typ == TypeProductTypeCreateV1 {
		// Creation intent: the entity does not exist yet, so the outer
		// entity_id must be the explicit absent marker ("") — never a
		// placeholder UUID pretending to be the business identity. The
		// requested stable key travels as payload "code" (validated shape
		// here, uniqueness Retail-side at apply).
		if strings.TrimSpace(entityID) != "" {
			return nil, fmt.Errorf("create command entity must be absent (\"\")")
		}
		if err := validateTypeCode(decoded["code"]); err != nil {
			return nil, err
		}
		if code, _ := decoded["code"].(string); code != "" {
			decoded["code"] = strings.ToLower(strings.TrimSpace(code))
		}
	} else {
		entity, ok := decoded[EntityKeyOf(typ)].(string)
		parsed, e := uuid.Parse(strings.TrimSpace(entity))
		outer, _ := uuid.Parse(strings.TrimSpace(entityID))
		if !ok || e != nil || parsed != outer {
			return nil, fmt.Errorf("command entity mismatch")
		}
		decoded[EntityKeyOf(typ)] = outer.String()
	}
	// Phase 17-R3 F16: ProductType identities in operator intent are Retail
	// source IDs (v4). A derived Store-scoped storage key is never intent.
	if EntityKeyOf(typ) == "product_type_id" && catalog.IsDerivedStorageIdentity(strings.ToLower(strings.TrimSpace(entityID))) {
		return nil, fmt.Errorf("invalid entity id: derived storage identity")
	}
	if typ == TypeProductTypeAssignV1 {
		target, ok := decoded["product_type_id"].(string)
		parsed, e := uuid.Parse(strings.TrimSpace(target))
		if !ok || e != nil || parsed == uuid.Nil {
			return nil, fmt.Errorf("invalid product_type_id: must be UUID")
		}
		if catalog.IsDerivedStorageIdentity(parsed.String()) {
			return nil, fmt.Errorf("invalid product_type_id: derived storage identity")
		}
		decoded["product_type_id"] = parsed.String()
	}
	key := ExpectedRevisionKeyOf(typ)
	if expectedRevision > 9007199254740991 {
		return nil, fmt.Errorf("invalid expected revision")
	}
	if v, present := decoded[key]; present {
		n, ok := v.(float64)
		if !ok || math.Trunc(n) != n || n != float64(expectedRevision) {
			return nil, fmt.Errorf("command revision mismatch")
		}
	} else {
		decoded[key] = float64(expectedRevision)
	}
	if err := boundPayload(typ, decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// validateTypeCode checks the requested stable type code shape (creation
// path only): 1..32 lowercase [a-z0-9_]. Retail re-validates at apply.
func validateTypeCode(raw any) error {
	code, _ := raw.(string)
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > 32 {
		return fmt.Errorf("invalid code: must be 1..32 chars")
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return fmt.Errorf("invalid code: lowercase letters, digits, underscore only")
		}
	}
	return nil
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
	// Phase 17-R2 type payloads: envelope bounds only (Retail owns
	// acceptance at apply). Dimensions/capabilities are bounded string
	// lists; codes are bounded here, validated canonically Retail-side.
	if typ == TypeProductTypeCreateV1 {
		if err := validateTypeCode(payload["code"]); err != nil {
			return err
		}
	}
	if typ == TypeProductTypeCreateV1 || typ == TypeProductTypeDimensionsUpdateV1 {
		if raw, present := payload["dimensions"]; present && raw != nil {
			list, ok := raw.([]any)
			if !ok || len(list) > 64 {
				return fmt.Errorf("invalid dimensions: at most 64")
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok || strings.TrimSpace(s) == "" || len(s) > 64 {
					return fmt.Errorf("invalid dimensions entry")
				}
			}
		}
	}
	if typ == TypeProductTypeCreateV1 || typ == TypeProductTypeCapabilitiesUpdateV1 {
		if raw, present := payload["capabilities"]; present && raw != nil {
			list, ok := raw.([]any)
			if !ok || len(list) > 64 {
				return fmt.Errorf("invalid capabilities: at most 64")
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok || strings.TrimSpace(s) == "" || len(s) > 64 {
					return fmt.Errorf("invalid capabilities entry")
				}
			}
		}
	}
	if typ == TypeProductVariantsUpdateV1 {
		raw, present := payload["variants"]
		if !present {
			return fmt.Errorf("missing variants")
		}
		list, ok := raw.([]any)
		if !ok || len(list) > 100 {
			return fmt.Errorf("invalid variants: at most 100")
		}
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid variants entry")
			}
			if err := boundVariantPayload(entry); err != nil {
				return err
			}
		}
	}
	if typ == TypeProductVariantUpdateV1 {
		if err := boundVariantPayload(payload); err != nil {
			return err
		}
	}
	if typ == TypeVariantAttributesUpdateV1 {
		raw, present := payload["attributes"]
		if !present {
			return fmt.Errorf("missing attributes")
		}
		if err := boundVariantAttributes(raw); err != nil {
			return err
		}
	}
	// Money travels as digit strings (never JSON numbers): float64
	// cannot represent values above 2^53, so the string-only rule is
	// enforced at creation as well as at Retail apply time. Null is
	// allowed only for explicitly nullable USD deltas.
	for _, key := range []string{"egp_price_cents", "usd_price_cents", "cost_cents", "egp_delta_cents", "price_egp_cents", "price_usd_cents"} {
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

// boundVariantPayload bounds one Phase 17 variant object: SKU and
// normalized combination identity plus the optional attribute list
// (money bounds come from the shared minor-string rule).
func boundVariantPayload(payload map[string]any) error {
	if err := variantStrLen(payload, "sku", 64); err != nil {
		return err
	}
	if err := variantStrLen(payload, "combination_key", 512); err != nil {
		return err
	}
	if raw, present := payload["attributes"]; present && raw != nil {
		return boundVariantAttributes(raw)
	}
	return nil
}

// variantStrLen bounds one optional string field of a variant object.
func variantStrLen(payload map[string]any, key string, max int) error {
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

// boundVariantAttributes bounds the Phase 17 option attribute list:
// identity codes, bilingual display labels, and positions.
func boundVariantAttributes(raw any) error {
	list, ok := raw.([]any)
	if !ok || len(list) > 32 {
		return fmt.Errorf("invalid attributes: at most 32")
	}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid attributes entry")
		}
		for _, key := range []string{"definition_code", "value_code"} {
			s, ok := entry[key].(string)
			if !ok || s == "" || len([]rune(s)) > 64 {
				return fmt.Errorf("invalid %s", key)
			}
		}
		for _, key := range []string{"name_ar", "definition_name_ar"} {
			s, ok := entry[key].(string)
			if !ok || strings.TrimSpace(s) == "" || len([]rune(s)) > 200 {
				return fmt.Errorf("invalid %s", key)
			}
		}
		for _, key := range []string{"name_en", "definition_name_en"} {
			if value, present := entry[key]; present && value != nil {
				s, ok := value.(string)
				if !ok || len([]rune(s)) > 200 {
					return fmt.Errorf("invalid %s", key)
				}
			}
		}
		if value, present := entry["position"]; present && value != nil {
			n, ok := value.(float64)
			if !ok || n < 0 || math.Trunc(n) != n {
				return fmt.Errorf("invalid position")
			}
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
		case TargetBlockedCapability:
			blocked++
		case TargetSkippedRevoked:
			rejected++
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

// ValidateOutcome checks bounded vocabulary and immutable command semantics.
func ValidateOutcome(cmd CommandView, status, code, entity string, pre, post int64) error {
	id, e := uuid.Parse(entity)
	if cmd.TargetKind == TargetKindCreate {
		// Only successful creation produces an entity. A failed create
		// truthfully reports no identity, rather than adopting a collision.
		if status == TargetApplied {
			if e != nil || id == uuid.Nil || strings.TrimSpace(entity) == "" {
				return fmt.Errorf("invalid outcome identity or revision")
			}
			// Retail mints v4 ProductType IDs; a derived storage key is
			// never a created entity (Phase 17-R3 F16).
			if catalog.IsDerivedStorageIdentity(id.String()) {
				return fmt.Errorf("invalid outcome identity or revision")
			}
		} else if entity != "" || pre != 0 || post != 0 {
			return fmt.Errorf("invalid outcome identity or revision")
		}
	} else {
		want, _ := uuid.Parse(cmd.EntityID)
		if e != nil || id != want || pre < 0 || post < 0 {
			return fmt.Errorf("invalid outcome identity or revision")
		}
	}
	if pre < 0 || post < 0 {
		return fmt.Errorf("invalid outcome identity or revision")
	}
	switch status {
	case TargetApplied:
		// A no-op may retain its revision for no-op-capable commands
		// (policy flips, configurations, Phase 17 variant updates):
		// convergence re-verifies the requested values in projection.
		noopCapable := cmd.Type == TypeProductOnlinePolicyUpdateV1 ||
			cmd.Type == TypeCategoryOnlinePolicyUpdateV1 ||
			cmd.Type == TypeProductConfigurationsUpdateV1 ||
			cmd.Type == TypeProductVariantsUpdateV1 ||
			cmd.Type == TypeProductVariantUpdateV1 ||
			cmd.Type == TypeVariantAttributesUpdateV1
		if code != CodeApplied || pre != cmd.ExpectedRevision || post < pre || post-pre > 2 || ((cmd.Type != TypeCategoryDetailsUpdateV1 && cmd.Type != TypeTagDetailsUpdateV1) && post-pre > 1) || (post == pre && !noopCapable) {
			return fmt.Errorf("invalid applied outcome")
		}
	case TargetConflict:
		if code != CodeConflict || post != 0 {
			return fmt.Errorf("invalid conflict outcome")
		}
	case TargetRejected:
		if post != 0 {
			return fmt.Errorf("invalid rejected outcome")
		}
		switch code {
		case CodeValidationFailed, CodeEntityNotFound, CodeStoreScopeConflict, CodeUnsupportedCommand, CodePayloadMismatch, CodeDependencyMissing:
		default:
			return fmt.Errorf("invalid rejection code")
		}
	default:
		return fmt.Errorf("invalid outcome status")
	}
	return nil
}
