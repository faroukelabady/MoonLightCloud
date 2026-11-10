package catalogadmin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestKnownTypesHaveNoGenericPatch(t *testing.T) {
	if IsKnownType("catalog.patch") {
		t.Fatal("generic patch must not exist")
	}
	for _, typ := range KnownTypes() {
		if !strings.HasSuffix(typ, ".v1") {
			t.Fatalf("%s must be versioned", typ)
		}
		if ExpectedRevisionKeyOf(typ) == "" {
			t.Fatalf("%s must map a revision key", typ)
		}
		if typ == TypeProductTypeCreateV1 {
			// R3 closure: creates address a not-yet-existing entity, so
			// they map NO entity key (the requested key is payload code).
			if EntityKeyOf(typ) != "" {
				t.Fatalf("%s must not map an entity key", typ)
			}
			continue
		}
		if EntityKeyOf(typ) == "" {
			t.Fatalf("%s must map an entity key", typ)
		}
	}
}

func TestHashPayloadKeyOrderIndependent(t *testing.T) {
	a, err := HashPayload([]byte(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPayload([]byte(`{"a":2,"b":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("hash must be canonical")
	}
	if len(a) != 64 {
		t.Fatal("hash must be hex sha256")
	}
}

func TestValidateNewCommandBounds(t *testing.T) {
	store := uuid.NewString()
	entity := uuid.NewString()
	payload := []byte(`{"product_id":"` + entity + `","expected_catalog_revision":3}`)
	if _, err := ValidateNewCommand(TypeProductDetailsUpdateV1, store, entity, 3, payload); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, tc := range map[string]struct {
		typ string
		sid string
		eid string
		rev int64
		raw []byte
	}{
		"unknown type":      {typ: "catalog.patch", sid: store, eid: entity, rev: 1, raw: payload},
		"bad store":         {typ: TypeProductDetailsUpdateV1, sid: "nope", eid: entity, rev: 1, raw: payload},
		"bad entity":        {typ: TypeProductDetailsUpdateV1, sid: store, eid: "nope", rev: 1, raw: payload},
		"negative revision": {typ: TypeProductDetailsUpdateV1, sid: store, eid: entity, rev: -1, raw: payload},
		"empty payload":     {typ: TypeProductDetailsUpdateV1, sid: store, eid: entity, rev: 1, raw: nil},
		"oversize payload":  {typ: TypeProductDetailsUpdateV1, sid: store, eid: entity, rev: 1, raw: make([]byte, 65*1024)},
		"non-object":        {typ: TypeProductDetailsUpdateV1, sid: store, eid: entity, rev: 1, raw: []byte(`[1,2]`)},
		"removed configs":   {typ: "catalog.product.configurations.update.v1", sid: store, eid: entity, rev: 1, raw: []byte(`{"configurations":[]}`)},
	} {
		_ = name
		if _, err := ValidateNewCommand(tc.typ, tc.sid, tc.eid, tc.rev, tc.raw); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
}

func TestAggregateNeverReportsSuccessEarly(t *testing.T) {
	if got := Aggregate(CommandPending, []string{TargetPending, TargetPending}, false); got != AggregatePending {
		t.Fatalf("pending: %s", got)
	}
	if got := Aggregate(CommandPending, []string{TargetDelivered}, false); got != AggregateDelivered {
		t.Fatalf("delivered: %s", got)
	}
	if got := Aggregate(CommandPending, []string{TargetApplied, TargetPending}, false); got != AggregatePending {
		t.Fatalf("partial-pending must not read applied: %s", got)
	}
	if got := Aggregate(CommandPending, []string{TargetApplied, TargetConflict}, false); got != AggregatePartial {
		t.Fatalf("mixed apply/conflict: %s", got)
	}
	if got := Aggregate(CommandPending, []string{TargetConflict}, false); got != AggregateConflict {
		t.Fatalf("conflict: %s", got)
	}
	if got := Aggregate(CommandPending, []string{TargetApplied, TargetApplied}, false); got != AggregateApplied {
		t.Fatalf("applied without convergence: %s", got)
	}
	if got := Aggregate(CommandPending, []string{TargetApplied, TargetApplied}, true); got != AggregateConverged {
		t.Fatalf("converged: %s", got)
	}
	if got := Aggregate(CommandCancelled, []string{TargetCancelled}, false); got != AggregateCancelled {
		t.Fatalf("cancelled: %s", got)
	}
	if got := Aggregate(CommandPending, []string{TargetBlockedCapability}, false); got != AggregateBlockedCapability {
		t.Fatalf("blocked capability: %s", got)
	}
	// Creation is never success: no input yields APPLIED/CONVERGED
	// without applied targets.
	if got := Aggregate(CommandPending, nil, true); got == AggregateApplied || got == AggregateConverged {
		t.Fatalf("empty must not succeed: %s", got)
	}
}

func TestHashVectorCrossRepo(t *testing.T) {
	// Pinned vector mirrored in Retail
	// (internal/service/admincommand/service_test.go): Cloud's
	// HashPayload must agree byte-for-byte, or every command
	// mismatches at Retail receipt adoption.
	raw := []byte(`{"entity_id":"11111111-1111-4111-8111-111111111111","expected_catalog_revision":5,"product_id":"11111111-1111-4111-8111-111111111111"}`)
	hash, err := HashPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "85ca2fbe21447b88757feec641dd5eff9887c97f267710f6796dbe15c11b766e" {
		t.Fatalf("vector hash: %s", hash)
	}
}

func TestStringMoneyEnforcedAtCreation(t *testing.T) {
	store := uuid.NewString()
	entity := uuid.NewString()
	base := map[string]any{"product_id": entity}
	withMoney := func(key string, value any) []byte {
		payload := map[string]any{"product_id": entity, key: value}
		_ = base
		raw, _ := json.Marshal(payload)
		return raw
	}
	for _, key := range []string{"egp_price_cents", "usd_price_cents", "cost_cents"} {
		good, err := ValidateNewCommand(TypeProductDetailsUpdateV1, store, entity, 1, withMoney(key, "9007199254740993"))
		if err != nil || good == nil {
			t.Fatalf("%s string money: %v", key, err)
		}
		if _, err := ValidateNewCommand(TypeProductDetailsUpdateV1, store, entity, 1, withMoney(key, float64(15000))); err == nil {
			t.Fatalf("%s numeric money must fail", key)
		}
		if _, err := ValidateNewCommand(TypeProductDetailsUpdateV1, store, entity, 1, withMoney(key, "-5")); err == nil {
			t.Fatalf("%s negative must fail", key)
		}
		if _, err := ValidateNewCommand(TypeProductDetailsUpdateV1, store, entity, 1, withMoney(key, "12.5")); err == nil {
			t.Fatalf("%s fractional must fail", key)
		}
	}
}

// These cases mirror Retail's nullable variantPrice/optMoney wire decoder.
// Full envelopes exercise both single commands and every entry in a bulk set.
func TestVariantMoneySingleAndBulkParity(t *testing.T) {
	store, product, variant := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, key := range []string{"price_egp_cents", "price_usd_cents"} {
		for _, tc := range []struct {
			name  string
			wire  json.RawMessage
			valid bool
		}{
			{"omitted inherits", nil, true},
			{"null inherits", json.RawMessage(`null`), true},
			{"zero overrides", json.RawMessage(`"0"`), true},
			{"leading zeroes", json.RawMessage(`"000000000000000000000000000000000000001"`), true},
			{"trimmed digits", json.RawMessage(`"  123  "`), true},
			{"beyond JS exact number", json.RawMessage(`"9007199254740993"`), true},
			{"int64 maximum", json.RawMessage(`"9223372036854775807"`), true},
			{"int64 overflow", json.RawMessage(`"9223372036854775808"`), false},
			{"nineteen digits overflow", json.RawMessage(`"9999999999999999999"`), false},
			{"long overflow", json.RawMessage(`"10000000000000000000"`), false},
			{"JSON integer", json.RawMessage(`0`), false},
			{"JSON unsafe integer", json.RawMessage(`9007199254740993`), false},
			{"JSON decimal", json.RawMessage(`1.5`), false},
			{"JSON exponent", json.RawMessage(`1e2`), false},
			{"boolean", json.RawMessage(`false`), false},
			{"object", json.RawMessage(`{}`), false},
			{"array", json.RawMessage(`[]`), false},
			{"empty", json.RawMessage(`""`), false},
			{"blank", json.RawMessage(`" "`), false},
			{"negative", json.RawMessage(`"-1"`), false},
			{"plus sign", json.RawMessage(`"+1"`), false},
			{"fraction", json.RawMessage(`"1.5"`), false},
			{"exponent string", json.RawMessage(`"1e2"`), false},
			{"non ASCII digits", json.RawMessage(`"١٢"`), false},
		} {
			for _, bulk := range []bool{false, true} {
				t.Run(key+"/"+tc.name+map[bool]string{false: "/single", true: "/bulk"}[bulk], func(t *testing.T) {
					entry := map[string]any{"sku": "OPTION-2", "is_active": true, "attributes": []any{}}
					if tc.wire != nil {
						entry[key] = tc.wire
					}
					typ, entity := TypeProductVariantUpdateV1, variant
					payload := entry
					if bulk {
						typ, entity = TypeProductVariantsUpdateV1, product
						payload = map[string]any{"product_id": product, "variants": []any{
							map[string]any{"sku": "OPTION-1", "is_active": true, "attributes": []any{}, "price_egp_cents": "10"}, entry,
						}}
					} else {
						entry["variant_id"] = variant
					}
					payload[ExpectedRevisionKeyOf(typ)] = 1
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					got, err := ValidateNewCommand(typ, store, entity, 1, raw)
					if (err == nil) != tc.valid {
						t.Fatalf("valid=%v want %v: %v", err == nil, tc.valid, err)
					}
					if tc.valid {
						encoded, err := json.Marshal(got)
						if err != nil {
							t.Fatal(err)
						}
						if string(encoded) != string(raw) {
							t.Fatalf("validation changed immutable payload: got %s want %s", encoded, raw)
						}
					}
				})
			}
		}
	}
}
