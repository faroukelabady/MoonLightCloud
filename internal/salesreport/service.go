package salesreport

import (
	"context"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/settings"
	"github.com/google/uuid"
)

// MaxDetailLimit bounds one page of the Sales details table.
const MaxDetailLimit = 200

// Period is the report window as inclusive local calendar dates.
type Period struct {
	Kind      string `json:"kind"`
	Timezone  string `json:"timezone"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

// SalesReport is the sales report response body.
type SalesReport struct {
	GeneratedAt     time.Time `json:"generated_at"`
	Period          Period    `json:"period"`
	StoreID         *string   `json:"store_id"`
	DisplayCurrency string    `json:"display_currency"`
	Shop            *Shop     `json:"shop"`
	// Settings are the business toggles the report was built with: tax
	// off removes tax from every total, as MoonLightRetail does.
	Settings settings.Business `json:"settings"`
	Filter   Filter            `json:"filter"`
	Sections
	DetailOffset int `json:"detail_offset"`
	DetailLimit  int `json:"detail_limit"`
}

// BusinessSettings reads the business-wide toggles.
type BusinessSettings interface {
	Business(ctx context.Context) (settings.Business, error)
}

// Service serves the printable reports.
type Service struct {
	repo     Repository
	settings BusinessSettings
}

// NewService wires the report service.
func NewService(repo Repository, business BusinessSettings) Service {
	return Service{repo: repo, settings: business}
}

// ParseFilter validates the report filters: comma-separated UUID lists
// and an optional payment method.
func ParseFilter(roots, subcategories, tags, payment string) (Filter, error) {
	f := Filter{Payment: strings.TrimSpace(payment)}
	switch f.Payment {
	case "", "cash", "card", "other":
	default:
		return Filter{}, apperr.New(apperr.InvalidInput, "payment_method must be cash, card or other")
	}
	var err error
	if f.RootIDs, err = uuidList(roots); err != nil {
		return Filter{}, err
	}
	if f.SubcategoryIDs, err = uuidList(subcategories); err != nil {
		return Filter{}, err
	}
	if f.TagIDs, err = uuidList(tags); err != nil {
		return Filter{}, err
	}
	return f, nil
}

func uuidList(raw string) ([]string, error) {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.ToLower(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		if _, err := uuid.Parse(id); err != nil || len(out) >= 100 {
			return nil, apperr.New(apperr.InvalidInput, "filter ids must be at most 100 UUIDs")
		}
		out = append(out, id)
	}
	return out, nil
}

// ParseDisplay validates the display currency (empty selects EGP, the
// Retail report's default).
func ParseDisplay(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", DisplayEGP:
		return DisplayEGP, nil
	case DisplayUSD:
		return DisplayUSD, nil
	case DisplayAll:
		return DisplayAll, nil
	default:
		return "", apperr.New(apperr.InvalidInput, "currency must be all, EGP or USD")
	}
}

// Sales builds the sales report for one validated period request.
func (s Service) Sales(ctx context.Context, req report.Request, display string, filter Filter, offset, limit int) (SalesReport, error) {
	if offset < 0 || limit < 1 || limit > MaxDetailLimit {
		return SalesReport{}, apperr.New(apperr.InvalidInput, "detail page out of range")
	}
	loc, err := time.LoadLocation(req.Period.Timezone)
	if err != nil {
		return SalesReport{}, apperr.Wrap(apperr.Internal, "report timezone", err)
	}
	facts, err := s.repo.SalesReportFacts(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.GeneratedAt(), display == DisplayUSD)
	if err != nil {
		return SalesReport{}, err
	}
	business, err := s.settings.Business(ctx)
	if err != nil {
		return SalesReport{}, err
	}
	opts := Options{Display: display, Loc: loc, Offset: offset, Limit: limit, Filter: filter, ExcludeTax: !business.TaxEnabled}
	sections, err := Build(facts, opts)
	if err != nil {
		return SalesReport{}, err
	}
	egp := sections
	if display != DisplayEGP {
		opts.Display, opts.Offset, opts.Limit = DisplayEGP, 0, 1
		if egp, err = Build(facts, opts); err != nil {
			return SalesReport{}, err
		}
	}
	sections.Base = baseOf(egp)
	if r := facts.LatestUSDRate; r != nil {
		sections.ExchangeRate.Latest = &RateView{Microrate: Money(r.Microrate), EffectiveAt: r.EffectiveAt, Source: r.Source}
	}
	return SalesReport{
		GeneratedAt: req.GeneratedAt(),
		Period: Period{
			Kind: req.Period.Kind, Timezone: req.Period.Timezone,
			StartDate: req.Period.StartLocal.Format("2006-01-02"),
			EndDate:   req.Period.EndLocalExclusive.AddDate(0, 0, -1).Format("2006-01-02"),
		},
		StoreID: req.StoreIDOrNil(), DisplayCurrency: display, Shop: facts.Shop,
		Settings: business, Filter: filter,
		Sections: sections, DetailOffset: offset, DetailLimit: limit,
	}, nil
}

// Stock builds the stocktaking valuation for the selected scope.
func (s Service) Stock(ctx context.Context, storeID string, now time.Time) (StockReport, error) {
	items, shop, err := s.repo.StockItems(ctx, storeID)
	if err != nil {
		return StockReport{}, err
	}
	totals, categories, rows, err := BuildStock(items)
	if err != nil {
		return StockReport{}, err
	}
	var scope *string
	if storeID != "" {
		scope = &storeID
	}
	return StockReport{GeneratedAt: now, StoreID: scope, Shop: shop, Totals: totals, Categories: categories, Items: rows}, nil
}

func errOverflow() error { return apperr.New(apperr.Internal, "report arithmetic overflow") }
