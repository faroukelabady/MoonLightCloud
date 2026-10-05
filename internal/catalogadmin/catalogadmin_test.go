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
		if EntityKeyOf(typ) == "" || ExpectedRevisionKeyOf(typ) == "" {
			t.Fatalf("%s must map entity + revision keys", typ)
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
		"too many configs":  {typ: TypeProductConfigurationsUpdateV1, sid: store, eid: entity, rev: 1, raw: []byte(`{"configurations":[` + strings.Repeat(`{},`, 101) + `{}]}`)},
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
