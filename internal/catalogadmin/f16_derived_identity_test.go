package catalogadmin

import (
	"testing"

	"github.com/google/uuid"
)

// Phase 17-R3 F16: a derived Store-scoped storage key (UUIDv5) is never
// ProductType intent or a created ProductType identity.
func TestF16DerivedStorageIdentityRefusedInAdminIntent(t *testing.T) {
	storeID := "77777777-0000-4000-8000-0000000000f1"
	productID := "77777777-0000-4000-8000-000000000001"
	retailType := "77777777-0000-4000-8000-000000000003"
	derived := uuid.NewSHA1(uuid.MustParse("d3f4a1b2-5c6d-4e7f-8a90-123456789abc"), []byte(storeID+":product-type:10000000-0000-4000-8000-000000000001")).String()

	details := func(id string) []byte {
		return []byte(`{"product_type_id":"` + id + `","name_ar":"كتب","name_en":"Books","expected_type_revision":1}`)
	}
	if _, err := ValidateNewCommand(TypeProductTypeDetailsUpdateV1, storeID, retailType, 1, details(retailType)); err != nil {
		t.Fatalf("Retail type identity must be accepted: %v", err)
	}
	if _, err := ValidateNewCommand(TypeProductTypeDetailsUpdateV1, storeID, derived, 1, details(derived)); err == nil {
		t.Fatal("derived storage key accepted as type command entity")
	}

	assign := func(id string) []byte {
		return []byte(`{"product_id":"` + productID + `","product_type_id":"` + id + `","expected_catalog_revision":3}`)
	}
	if _, err := ValidateNewCommand(TypeProductTypeAssignV1, storeID, productID, 3, assign(retailType)); err != nil {
		t.Fatalf("assign to Retail type identity must be accepted: %v", err)
	}
	if _, err := ValidateNewCommand(TypeProductTypeAssignV1, storeID, productID, 3, assign("10000000-0000-4000-8000-000000000001")); err != nil {
		t.Fatalf("assign to the fixed seed must be accepted: %v", err)
	}
	for name, id := range map[string]string{"derived": derived, "non-uuid": "papyrus", "nil": uuid.Nil.String()} {
		if _, err := ValidateNewCommand(TypeProductTypeAssignV1, storeID, productID, 3, assign(id)); err == nil {
			t.Fatalf("assign target %s accepted", name)
		}
	}

	create := CommandView{Type: TypeProductTypeCreateV1, TargetKind: TargetKindCreate, ExpectedRevision: 0}
	if err := ValidateOutcome(create, TargetApplied, CodeApplied, retailType, 0, 1); err != nil {
		t.Fatalf("Retail-minted create result rejected: %v", err)
	}
	if err := ValidateOutcome(create, TargetApplied, CodeApplied, derived, 0, 1); err == nil {
		t.Fatal("derived storage key accepted as created identity")
	}
}
