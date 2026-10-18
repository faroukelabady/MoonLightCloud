package orders

import (
	"errors"
	"reflect"
	"testing"
)

func TestOrderStoreEvidence(t *testing.T) {
	productA, productB := "product-a", "product-b"
	storeA, storeB, blank := "store-a", "store-b", ""
	for _, tc := range []struct {
		name   string
		lines  []OrderLine
		stores map[string]*string
		want   *string
		mixed  bool
	}{
		{"no lines", nil, nil, nil, false},
		{"unmapped carries no evidence", []OrderLine{{MoonlightProduct: &productA}}, map[string]*string{productA: &storeA}, nil, false},
		{"mapped without product", []OrderLine{{Mapped: true}}, nil, nil, false},
		{"one proven store", []OrderLine{{Mapped: true, MoonlightProduct: &productA}}, map[string]*string{productA: &storeA}, &storeA, false},
		{"unanimous", []OrderLine{{Mapped: true, MoonlightProduct: &productA}, {Mapped: true, MoonlightProduct: &productB}}, map[string]*string{productA: &storeA, productB: &storeA}, &storeA, false},
		{"mixed stores", []OrderLine{{Mapped: true, MoonlightProduct: &productA}, {Mapped: true, MoonlightProduct: &productB}}, map[string]*string{productA: &storeA, productB: &storeB}, nil, true},
		{"missing evidence prevents adoption", []OrderLine{{Mapped: true, MoonlightProduct: &productA}, {Mapped: true, MoonlightProduct: &productB}}, map[string]*string{productA: &storeA}, nil, false},
		{"null evidence prevents adoption", []OrderLine{{Mapped: true, MoonlightProduct: &productA}, {Mapped: true, MoonlightProduct: &productB}}, map[string]*string{productA: &storeA, productB: nil}, nil, false},
		{"blank evidence prevents adoption", []OrderLine{{Mapped: true, MoonlightProduct: &productA}, {Mapped: true, MoonlightProduct: &productB}}, map[string]*string{productA: &storeA, productB: &blank}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, mixed := DeriveOrderStore(OrderSnapshot{Lines: tc.lines}, tc.stores)
			if !reflect.DeepEqual(got, tc.want) || mixed != tc.mixed {
				t.Fatalf("store = %v, mixed = %v; want %v, %v", got, mixed, tc.want, tc.mixed)
			}
		})
	}
}

func TestExistingOrderStoreDecision(t *testing.T) {
	a, b := "store-a", "store-b"
	for _, tc := range []struct {
		name              string
		existing, derived *string
		mixed, tombstone  bool
		want              *string
		changed, blocked  bool
	}{
		{"unproven", nil, nil, false, false, nil, false, false},
		{"adopt", nil, &a, false, false, &a, true, false},
		{"keep proven without evidence", &a, nil, false, false, &a, false, false},
		{"same ownership", &a, &a, false, false, &a, false, false},
		{"cross store", &a, &b, false, false, nil, false, true},
		{"mixed cannot adopt", nil, &a, true, false, nil, false, true},
		{"mixed cannot update", &a, &a, true, false, nil, false, true},
		{"tombstone keeps ownership", &a, &b, true, true, &a, false, false},
		{"tombstone with no evidence", &a, nil, false, true, &a, false, false},
		{"unproven tombstone", nil, nil, true, true, nil, false, false},
		{"tombstone may retain evidence", nil, &a, false, true, &a, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := DecideOrderStore(tc.existing, tc.derived, tc.mixed, tc.tombstone)
			assertStoreDecision(t, got, tc.want, err, tc.blocked)
			if changed != tc.changed {
				t.Fatalf("ownership changed = %v, want %v", changed, tc.changed)
			}
		})
	}
}

func TestNewOrderStoreDecision(t *testing.T) {
	a := "store-a"
	for _, derived := range []*string{nil, &a} {
		for _, mixed := range []bool{false, true} {
			for _, tombstone := range []bool{false, true} {
				got, err := DecideNewOrderStore(derived, mixed, tombstone)
				blocked := mixed && !tombstone
				want := derived
				if blocked {
					want = nil
				}
				assertStoreDecision(t, got, want, err, blocked)
			}
		}
	}
}

func assertStoreDecision(t *testing.T, got, want *string, err error, blocked bool) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ownership = %v, want %v", got, want)
	}
	if !blocked {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var failure *BlockedError
	if !errors.As(err, &failure) || failure.Code != CodeCommerceStoreScopeConflict {
		t.Fatalf("expected bounded Store conflict, got %v", err)
	}
}
