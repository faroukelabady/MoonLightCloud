package dashboard

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"math"
	"strings"
	"testing"
	"time"
)

func TestOrderAnalyticsOverflowFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   []OrderAnalyticsRowRaw
		want   string
		failed bool
	}{
		{"above 2^53", []OrderAnalyticsRowRaw{{ProviderKey: "a", Currency: "EGP", CanonicalStatus: "PENDING", ValueMinor: 9007199254740993}}, "9007199254740993", false},
		{"exact boundary", []OrderAnalyticsRowRaw{{ProviderKey: "a", Currency: "EGP", CanonicalStatus: "PENDING", ValueMinor: math.MaxInt64 - 1}, {ProviderKey: "a", Currency: "EGP", CanonicalStatus: "COMPLETED", ValueMinor: 1}}, "9223372036854775807", false},
		{"across statuses", []OrderAnalyticsRowRaw{{ProviderKey: "a", Currency: "EGP", CanonicalStatus: "PENDING", ValueMinor: 5000000000000000000}, {ProviderKey: "a", Currency: "EGP", CanonicalStatus: "COMPLETED", ValueMinor: 5000000000000000000}}, "", true},
		{"across providers", []OrderAnalyticsRowRaw{{ProviderKey: "a", Currency: "EGP", ValueMinor: math.MaxInt64}, {ProviderKey: "b", Currency: "EGP", ValueMinor: 1}}, "", true},
		{"separate currencies", []OrderAnalyticsRowRaw{{ProviderKey: "a", Currency: "EGP", ValueMinor: math.MaxInt64}, {ProviderKey: "a", Currency: "USD", ValueMinor: math.MaxInt64}}, "9223372036854775807", false},
		// Signed defensive fixtures isolate each checked sub-aggregate; production
		// provider order values remain subject to their existing validation/schema.
		{"active only", []OrderAnalyticsRowRaw{{ProviderKey: "a", Currency: "EGP", CanonicalStatus: "PENDING", ValueMinor: math.MaxInt64}, {ProviderKey: "a", Currency: "EGP", CanonicalStatus: "CANCELLED", ValueMinor: -1}, {ProviderKey: "a", Currency: "EGP", CanonicalStatus: "COMPLETED", ValueMinor: 1}}, "", true},
		{"provider only", []OrderAnalyticsRowRaw{{ProviderKey: "a", Currency: "EGP", ValueMinor: math.MaxInt64}, {ProviderKey: "b", Currency: "EGP", ValueMinor: -1}, {ProviderKey: "a", Currency: "EGP", ValueMinor: 1}}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubAnalyticsRepo{orders: tc.rows}
			svc := NewService(report.NewService(repo, clock.System{}, time.UTC), repo, nil, clock.System{})
			out, err := svc.OrderAnalytics(context.Background(), analyticsRequest(), "")
			if tc.failed {
				if err == nil || len(out.CurrencyTotals) != 0 {
					t.Fatalf("overflow returned successful aggregate: %+v %v", out, err)
				}
				if strings.Contains(err.Error(), "5000000000000000000") {
					t.Fatal("raw amount exposed")
				}
			} else {
				if err != nil || out.CurrencyTotals[0].ValueMinor != tc.want {
					t.Fatal(out, err)
				}
			}
		})
	}
}
