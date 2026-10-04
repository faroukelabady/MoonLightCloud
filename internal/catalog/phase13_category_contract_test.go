package catalog

// Phase 13 §27-§34: dual-version category sync contract.

import (
	"encoding/json"
	"testing"
)

func raw(payload string) json.RawMessage { return json.RawMessage(payload) }

// §28/§30: v1 is immutable history and normalizes to online_enabled=true
// (exact pre-Phase13 semantics).
func TestCategorySnapshotV1NormalizesOnlineEnabled(t *testing.T) {
	decoded, err := DecodeCategorySnapshot(raw(`{"category_id":"aaaaaaaa-0000-4000-8000-000000000001","status":"active","names":[{"locale":"en","name":"A"}],"parent_ids":[],"catalog_revision":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.OnlineEnabled {
		t.Fatal("v1 must normalize to online_enabled = true")
	}
}

// §132: v2 carries the policy explicitly; missing required field and
// malformed types fail closed.
func TestCategorySnapshotV2PolicyRequired(t *testing.T) {
	for _, payload := range []string{
		`{"category_id":"aaaaaaaa-0000-4000-8000-000000000001","status":"active","names":[{"locale":"en","name":"A"}],"parent_ids":[],"online_enabled":true,"catalog_revision":5}`,
		`{"category_id":"aaaaaaaa-0000-4000-8000-000000000001","status":"active","names":[{"locale":"en","name":"A"}],"parent_ids":[],"online_enabled":false,"catalog_revision":5}`,
	} {
		if _, err := DecodeCategorySnapshotV2(raw(payload)); err != nil {
			t.Fatalf("valid v2 rejected: %v", err)
		}
	}
	disabled, err := DecodeCategorySnapshotV2(raw(`{"category_id":"aaaaaaaa-0000-4000-8000-000000000001","status":"active","names":[{"locale":"en","name":"A"}],"parent_ids":[],"online_enabled":false,"catalog_revision":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if disabled.OnlineEnabled {
		t.Fatal("v2 false must decode as false")
	}

	missing := `{"category_id":"aaaaaaaa-0000-4000-8000-000000000001","status":"active","names":[{"locale":"en","name":"A"}],"parent_ids":[],"catalog_revision":5}`
	if _, err := DecodeCategorySnapshotV2(raw(missing)); err == nil {
		t.Fatal("missing online_enabled must fail closed")
	}
	malformed := `{"category_id":"aaaaaaaa-0000-4000-8000-000000000001","status":"active","names":[{"locale":"en","name":"A"}],"parent_ids":[],"online_enabled":"yes","catalog_revision":5}`
	if _, err := DecodeCategorySnapshotV2(raw(malformed)); err == nil {
		t.Fatal("non-boolean online_enabled must fail closed")
	}
}

// §34: the semantic fingerprint is policy-aware — different policy can
// never be fingerprint-equivalent.
func TestCategoryFingerprintIsPolicyAware(t *testing.T) {
	base := CategorySnapshot{CategoryID: "aaaaaaaa-0000-4000-8000-000000000001", Status: "active",
		Names: []CatalogName{{Locale: "en", Name: "A"}}, CatalogRevision: 5, OnlineEnabled: true}
	disabled := base
	disabled.OnlineEnabled = false

	enabledTwice := base
	enabledTwice.OnlineEnabled = true

	if FingerprintCategory(base) != FingerprintCategory(enabledTwice) {
		t.Fatal("identical state must fingerprint identically")
	}
	if FingerprintCategory(base) == FingerprintCategory(disabled) {
		t.Fatal("policy difference must change the fingerprint")
	}
}
