package orders

import (
	"strings"
	"testing"
	"time"
)

func TestParseMinorUnits(t *testing.T) {
	cases := []struct {
		input string
		want  int64
		fail  bool
	}{
		{"0", 0, false},
		{"0.00", 0, false},
		{"13.00", 1300, false},
		{"650.25", 65025, false},
		{"650.2", 65020, false},
		{"90071992547409.93", 9007199254740993, false},
		{"-5.00", -500, false},
		{"", 0, true},
		{"abc", 0, true},
		{"12.345", 0, true},
		{"12.3.4", 0, true},
		{".5", 0, true},
		{"13.", 0, true},
		{"99999999999999999999.00", 0, true},
		{"1e3", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseMinorUnits(tc.input, "EGP")
			if tc.fail {
				if err == nil {
					t.Fatalf("must fail, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestMapWooStatus(t *testing.T) {
	cases := map[string]CanonicalStatus{
		"pending": StatusPending, "processing": StatusProcessing, "on-hold": StatusOnHold,
		"on_hold": StatusOnHold, "completed": StatusCompleted, "cancelled": StatusCancelled,
		"canceled": StatusCancelled, "refunded": StatusRefunded, "failed": StatusFailed,
		"awaiting-pickup-custom": StatusUnknown, "trash": StatusUnknown, "": StatusUnknown,
		"Completed": StatusCompleted,
	}
	for input, want := range cases {
		if got := MapWooStatus(input); got != want {
			t.Fatalf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestParseWebhookTopic(t *testing.T) {
	for _, valid := range []string{"order.created", "order.updated", "order.deleted"} {
		topic, err := ParseWebhookTopic(valid)
		if err != nil || string(topic) != valid {
			t.Fatalf("%q: %v", valid, err)
		}
	}
	if _, err := ParseWebhookTopic("product.updated"); err == nil {
		t.Fatal("product topic must fail")
	}
	if !TopicOrderDeleted.IsDeletion() || TopicOrderCreated.IsDeletion() {
		t.Fatal("deletion predicate")
	}
}

func TestValidateDeliveryID(t *testing.T) {
	if err := ValidateDeliveryID("12345"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", strings.Repeat("x", 201), "has\nnewline"} {
		if err := ValidateDeliveryID(invalid); err == nil {
			t.Fatalf("%q must fail", invalid)
		}
	}
}

func TestCanonicalExternalOrderID(t *testing.T) {
	got, err := CanonicalExternalOrderID("00042")
	if err != nil || got != "42" {
		t.Fatalf("canonical: %q %v", got, err)
	}
	for _, invalid := range []string{"", "0", "-3", "abc", "12x"} {
		if _, err := CanonicalExternalOrderID(invalid); err == nil {
			t.Fatalf("%q must fail", invalid)
		}
	}
}

func baseSnapshot() OrderSnapshot {
	created := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	return OrderSnapshot{
		ProviderKey: "website", ExternalOrderID: "100", OrderNumber: "100",
		ProviderStatus: "processing", Canonical: StatusProcessing,
		Currency: "EGP", TotalMinor: 12000,
		CreatedAt: created, ModifiedAt: modified,
		Customer: Customer{FirstName: "أحمد", Email: "a@example.com"},
		Lines: []OrderLine{
			{ExternalLineID: 1, ExternalProductID: "500", SKU: "PAP-1", Name: "بردية", Quantity: 1, TotalMinor: 12000, Mapped: true},
		},
		MappingComplete: true,
	}
}

func TestFingerprintStable(t *testing.T) {
	a, b := baseSnapshot(), baseSnapshot()
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("identical snapshots must share fingerprint")
	}
}

func TestFingerprintChanges(t *testing.T) {
	base := Fingerprint(baseSnapshot())
	mutations := map[string]func(*OrderSnapshot){
		"status":      func(s *OrderSnapshot) { s.Canonical = StatusCompleted; s.ProviderStatus = "completed" },
		"money":       func(s *OrderSnapshot) { s.TotalMinor++ },
		"line qty":    func(s *OrderSnapshot) { s.Lines[0].Quantity++ },
		"line mapped": func(s *OrderSnapshot) { s.Lines[0].Mapped = false; s.Lines[0].MoonlightProduct = nil },
		"address":     func(s *OrderSnapshot) { s.Shipping.City = "Giza" },
		"customer":    func(s *OrderSnapshot) { s.Customer.Email = "b@example.com" },
		"modified":    func(s *OrderSnapshot) { s.ModifiedAt = s.ModifiedAt.Add(time.Second) },
		"deleted":     func(s *OrderSnapshot) { s.ProviderDeleted = true; s.Canonical = StatusDeleted },
	}
	for name, mutate := range mutations {
		snapshot := baseSnapshot()
		mutate(&snapshot)
		if Fingerprint(snapshot) == base {
			t.Fatalf("%s must change fingerprint", name)
		}
	}
}
