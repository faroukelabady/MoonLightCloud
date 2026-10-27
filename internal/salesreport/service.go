package salesreport

import (
	"context"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
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
	Sections
	DetailOffset int `json:"detail_offset"`
	DetailLimit  int `json:"detail_limit"`
}

// Service serves the printable reports.
type Service struct {
	repo Repository
}

// NewService wires the report service.
func NewService(repo Repository) Service { return Service{repo: repo} }

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
func (s Service) Sales(ctx context.Context, req report.Request, display string, offset, limit int) (SalesReport, error) {
	if offset < 0 || limit < 1 || limit > MaxDetailLimit {
		return SalesReport{}, apperr.New(apperr.InvalidInput, "detail page out of range")
	}
	loc, err := time.LoadLocation(req.Period.Timezone)
	if err != nil {
		return SalesReport{}, apperr.Wrap(apperr.Internal, "report timezone", err)
	}
	facts, err := s.repo.SalesReportFacts(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, display == DisplayUSD)
	if err != nil {
		return SalesReport{}, err
	}
	sections, err := Build(facts, display, loc, offset, limit)
	if err != nil {
		return SalesReport{}, err
	}
	egp := sections
	if display != DisplayEGP {
		if egp, err = Build(facts, DisplayEGP, loc, 0, 1); err != nil {
			return SalesReport{}, err
		}
	}
	sections.Base = baseOf(egp)
	return SalesReport{
		GeneratedAt: req.GeneratedAt(),
		Period: Period{
			Kind: req.Period.Kind, Timezone: req.Period.Timezone,
			StartDate: req.Period.StartLocal.Format("2006-01-02"),
			EndDate:   req.Period.EndLocalExclusive.AddDate(0, 0, -1).Format("2006-01-02"),
		},
		StoreID: req.StoreIDOrNil(), DisplayCurrency: display, Shop: facts.Shop,
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
