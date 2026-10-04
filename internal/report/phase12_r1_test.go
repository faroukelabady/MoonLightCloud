package report

import (
	"context"
	"fmt"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

type tagRankRepo struct {
	stubRankRepo
	tags []TagRow
}

func (s tagRankRepo) SalesByTag(context.Context, time.Time, time.Time, string) ([]TagRow, error) {
	return s.tags, nil
}
func (s tagRankRepo) RefundsByTag(context.Context, time.Time, time.Time, string) ([]RefundTagRow, error) {
	return nil, nil
}
func TestTopTagHistoricalIdentityStableAcrossInputOrder(t *testing.T) {
	var rows []TagRow
	for i := 0; i < 120; i++ {
		rows = append(rows, TagRow{ID: fmt.Sprintf("%03d", i), Slug: "same", NameAR: "متساو", NameEN: "Same", Units: 1, Currency: "EGP", Sales: 100})
	}
	rows = append(rows, TagRow{ID: "000", Slug: "same", NameAR: "متساو", NameEN: "Renamed", Units: 1, Currency: "EGP", Sales: 100})
	rows = append(rows, TagRow{ID: "000", Slug: "different", NameAR: "متساو", NameEN: "Same", Units: 1, Currency: "EGP", Sales: 100}, TagRow{ID: "000", Slug: "same", NameAR: "اسم تاريخي", NameEN: "Same", Units: 1, Currency: "EGP", Sales: 100})
	for _, row := range append([]TagRow(nil), rows...) {
		row.Currency = "USD"
		rows = append(rows, row)
	}
	req := Request{Period: Period{Kind: "today", Timezone: "UTC", StartUTC: time.Unix(0, 0), EndUTC: time.Unix(100, 0)}, now: time.Unix(50, 0)}
	rng := rand.New(rand.NewSource(1))
	for _, currency := range []string{"", "EGP", "USD"} {
		for _, limit := range []int{1, 10, 100} {
			req.Currency, req.Limit = currency, limit
			var want []string
			for iteration := 0; iteration < 30; iteration++ {
				rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
				svc := NewService(tagRankRepo{tags: rows}, clock.System{}, time.UTC)
				out, err := svc.Breakdown(context.Background(), req, DimensionTag)
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, r := range out.Rows {
					got = append(got, fmt.Sprintf("%s/%s/%s/%s", ptrStr(r.TagID), ptrStr(r.TagSlug), ptrStr(r.NameAR), ptrStr(r.NameEN)))
				}
				if want == nil {
					want = got
				} else if !reflect.DeepEqual(got, want) {
					t.Fatalf("currency=%s limit=%d shuffled membership %v != %v", currency, limit, got, want)
				}
				if len(got) != limit {
					t.Fatalf("limit=%d got=%d", limit, len(got))
				}
			}
		}
	}
}
