package catalogadmin

import (
	"testing"
)

// Phase 17-R2 (R13) type-command parity: the EXACT type command payloads
// MoonLightRetail's dispatcher consumes
// (MoonLightRetail internal/service/admincommand/product_type_commands_test.go)
// must be creatable here — the same mutation intent is accepted by Cloud's
// creation-time validation and applied by Retail. Any drift in field
// names, code shapes, or revision keys breaks one side's suite.
func TestPhase17R2CrossRepoTypeCommandParity(t *testing.T) {
	storeID := "77777777-0000-4000-8000-0000000000f1"
	typeID := "77777777-0000-4000-8000-000000000003"
	productID := "77777777-0000-4000-8000-000000000001"

	createPayload := `{
		"code": "book", "name_ar": "كتب", "name_en": "Book",
		"dimensions": ["color"], "capabilities": [],
		"expected_type_revision": 0
	}`
	// R3 closure: creates carry NO business entity — the absent marker ""
	// is the only accepted entity_id (placeholder UUIDs are rejected).
	if _, err := ValidateNewCommand(TypeProductTypeCreateV1, storeID,
		"", 0, []byte(createPayload)); err != nil {
		t.Fatalf("type create command must be creatable: %v", err)
	}
	if _, err := ValidateNewCommand(TypeProductTypeCreateV1, storeID,
		"00000000-0000-4000-8000-000000000000", 0, []byte(createPayload)); err == nil {
		t.Fatal("placeholder entity UUID must be rejected for creates")
	}
	if _, err := ValidateNewCommand(TypeProductTypeCreateV1, storeID,
		"", 0, []byte(`{"code": "BAD CODE", "expected_type_revision": 0}`)); err == nil {
		t.Fatal("bad code must be rejected at creation")
	}

	detailsPayload := `{
		"product_type_id": "` + typeID + `", "name_ar": "كتب", "name_en": "Books",
		"expected_type_revision": 1
	}`
	if _, err := ValidateNewCommand(TypeProductTypeDetailsUpdateV1, storeID,
		typeID, 1, []byte(detailsPayload)); err != nil {
		t.Fatalf("type details command must be creatable: %v", err)
	}

	dimensionsPayload := `{
		"product_type_id": "` + typeID + `", "dimensions": ["color"],
		"expected_type_revision": 2
	}`
	if _, err := ValidateNewCommand(TypeProductTypeDimensionsUpdateV1, storeID,
		typeID, 2, []byte(dimensionsPayload)); err != nil {
		t.Fatalf("type dimensions command must be creatable: %v", err)
	}

	capabilitiesPayload := `{
		"product_type_id": "` + typeID + `", "capabilities": [],
		"expected_type_revision": 2
	}`
	if _, err := ValidateNewCommand(TypeProductTypeCapabilitiesUpdateV1, storeID,
		typeID, 2, []byte(capabilitiesPayload)); err != nil {
		t.Fatalf("type capabilities command must be creatable: %v", err)
	}

	statusPayload := `{
		"product_type_id": "` + typeID + `", "is_active": false,
		"expected_type_revision": 2
	}`
	if _, err := ValidateNewCommand(TypeProductTypeStatusUpdateV1, storeID,
		typeID, 2, []byte(statusPayload)); err != nil {
		t.Fatalf("type status command must be creatable: %v", err)
	}

	assignPayload := `{
		"product_id": "` + productID + `", "product_type_id": "` + typeID + `",
		"expected_catalog_revision": 5
	}`
	if _, err := ValidateNewCommand(TypeProductTypeAssignV1, storeID,
		productID, 5, []byte(assignPayload)); err != nil {
		t.Fatalf("type assign command must be creatable: %v", err)
	}

	// Revision keys: type stream vs product stream for assign.
	if got := ExpectedRevisionKeyOf(TypeProductTypeDetailsUpdateV1); got != "expected_type_revision" {
		t.Fatalf("type revision key: %q", got)
	}
	if got := ExpectedRevisionKeyOf(TypeProductTypeAssignV1); got != "expected_catalog_revision" {
		t.Fatalf("assign revision key: %q", got)
	}
	if got := EntityKeyOf(TypeProductTypeAssignV1); got != "product_id" {
		t.Fatalf("assign entity key: %q", got)
	}
}
