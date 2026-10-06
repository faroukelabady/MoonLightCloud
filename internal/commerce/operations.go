package commerce

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"math"
)

// ProductOperationKey derives the deterministic retry identity for one
// desired product state: provider key, product ID, catalog revision,
// policy revision, publication state, and the Category ONLINE policy
// identity (Phase 13 §96: eligibility fingerprint + policy version).
// Same desired state retried after a temporary failure yields the same
// key; any state change yields a new key. The policy VERSION advances
// across an enabled->disabled->enabled cycle, so that cycle can never
// reuse the original generation's provider idempotency key. No
// timestamps, random IDs, or attempt counters.
func ProductOperationKey(providerKey ProviderKey, productID string, catalogRevision, policyRevision int64, published bool, categoryPolicyFingerprint, categoryPolicyVersion, configurationsFingerprint, configurationsVersion string) string {
	return operationKey("product",
		string(providerKey), productID,
		revisionBytes(catalogRevision), revisionBytes(policyRevision),
		boolBytes(published), categoryPolicyFingerprint, categoryPolicyVersion,
		configurationsFingerprint, configurationsVersion,
	)
}

// InventoryOperationKey derives the deterministic retry identity for one
// desired inventory state: product ID, the revisions behind the computed
// availability (catalog lifecycle, sales policy, inventory), the derived
// quantity, and publication state.
func InventoryOperationKey(providerKey ProviderKey, productID string, catalogRevision, policyRevision, inventoryRevision, quantity int64, published bool, categoryPolicyFingerprint, categoryPolicyVersion, configurationsFingerprint, configurationsVersion string) string {
	return operationKey("inventory",
		string(providerKey), productID,
		revisionBytes(catalogRevision), revisionBytes(policyRevision), revisionBytes(inventoryRevision),
		revisionBytes(quantity), boolBytes(published), categoryPolicyFingerprint, categoryPolicyVersion,
		configurationsFingerprint, configurationsVersion,
	)
}

// VariantOperationKey derives the deterministic retry identity for one
// desired ProductVariant set (Phase 17): provider key, product ID, the
// catalog/policy revisions, publication state, and the deterministic
// variant-set identity (fingerprint + version). Same desired state
// retried after a temporary failure yields the same key.
func VariantOperationKey(providerKey ProviderKey, productID string, catalogRevision, policyRevision int64, published bool, variantsFingerprint, variantsVersion string) string {
	return operationKey("product-variants",
		string(providerKey), productID,
		revisionBytes(catalogRevision), revisionBytes(policyRevision),
		boolBytes(published), variantsFingerprint, variantsVersion,
	)
}

// VariantInventoryOperationKey derives the deterministic retry identity
// for ONE variant's desired availability: the variant's own identity and
// inventory revision plus the derived quantity. Per-variant keys keep
// concurrent variant publishes independent and idempotent.
func VariantInventoryOperationKey(providerKey ProviderKey, productID, variantID string, catalogRevision, policyRevision, inventoryRevision, quantity int64, ready, published bool, variantsFingerprint, variantsVersion string) string {
	return operationKey("variant-inventory",
		string(providerKey), productID, variantID,
		revisionBytes(catalogRevision), revisionBytes(policyRevision), revisionBytes(inventoryRevision),
		revisionBytes(quantity), boolBytes(ready), boolBytes(published),
		variantsFingerprint, variantsVersion,
	)
}

// operationKey hashes canonical length-prefixed fields: domain separation
// plus explicit lengths, so no concatenation ambiguity and no unordered
// JSON anywhere near retry identity.
func operationKey(domain string, fields ...string) string {
	h := sha256.New()
	writeField(h, domain)
	for _, field := range fields {
		writeField(h, field)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

func writeField(h hash.Hash, field string) {
	var length [8]byte
	if len(field) > math.MaxUint32 {
		panic("commerce: operation key field too long")
	}
	binary.BigEndian.PutUint64(length[:], uint64(len(field)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(field))
}

func revisionBytes(revision int64) string {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(revision))
	return string(encoded[:])
}

func boolBytes(value bool) string {
	if value {
		return "\x01"
	}
	return "\x00"
}
