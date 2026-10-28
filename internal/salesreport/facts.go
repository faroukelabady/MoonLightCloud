// Package salesreport builds the printable dashboard reports: the sales
// report (the same sections MoonLightRetail prints) and the stocktaking
// valuation behind the zakat calculator. The adapter returns facts at
// sale-line grain; this package converts currencies and aggregates them
// in Go, so every section follows one exact rounding rule.
package salesreport

import (
	"context"
	"time"
)

// Sale is one projected Sale in the report's sale set.
type Sale struct {
	ID         string
	Number     string
	OccurredAt time.Time
	Currency   string
	Subtotal   int64
	Discount   int64
	Tax        int64
	Total      int64
	// FxMicrorate is the USD->EGP snapshot a USD Sale carries (0 for EGP).
	FxMicrorate int64
	// InPeriod is false for a Sale outside the period that a return in
	// the period refers to: it only supplies line context and its rate.
	InPeriod bool
	// USDRateMicrorate is the Store's USD->EGP rate nearest an EGP Sale,
	// used to show it in USD; 0 when unknown or not requested.
	USDRateMicrorate int64
}

// Line is one Sale line; UnitCost is nil when Retail had no cost.
type Line struct {
	SaleID      string
	ItemID      string
	ProductID   string
	SKU         string
	ProductName string
	Quantity    int64
	LineTotal   int64
	UnitCost    *int64
}

// Classification is a line's historical root category or subcategory.
type Classification struct {
	SaleID string
	ItemID string
	Kind   string // "root" | "subcategory"
	ID     string
	NameAr string
	NameEn string
}

// Tag is a line's historical Tag snapshot.
type Tag struct {
	SaleID string
	ItemID string
	ID     string
	NameAr string
	NameEn string
}

// Payment is one Sale payment; the applied amount is Amount - Change.
type Payment struct {
	SaleID string
	Method string
	Amount int64
	Change int64
}

// Return is one return or void recorded in the period.
type Return struct {
	ID         string
	SaleID     string
	OccurredAt time.Time
	Currency   string
	Tax        int64
	Total      int64
}

// ReturnLine is one returned Sale line; Cost is the line's total cost.
type ReturnLine struct {
	ReturnID   string
	SaleID     string
	SaleLineID string
	Quantity   int64
	Restocked  bool
	Refund     int64
	Cost       *int64
}

// ReturnPayment is one refund payment.
type ReturnPayment struct {
	ReturnID string
	Method   string
	Amount   int64
}

// Shop is the letterhead from the newest Sale in scope.
type Shop struct {
	NameAr    string `json:"name_ar"`
	NameEn    string `json:"name_en"`
	AddressAr string `json:"address_ar"`
	AddressEn string `json:"address_en"`
	Phone     string `json:"phone"`
}

// Rate is one USD->EGP rate and when it took effect; Source is "cloud"
// (Cloud settings) or "sale" (a synced USD Sale's snapshot).
type Rate struct {
	Microrate   int64
	EffectiveAt time.Time
	Source      string
}

// Facts is everything one sales report reads.
type Facts struct {
	Sales           []Sale
	Lines           []Line
	Classifications []Classification
	Tags            []Tag
	Payments        []Payment
	Returns         []Return
	ReturnLines     []ReturnLine
	ReturnPayments  []ReturnPayment
	Shop            *Shop
	// LatestUSDRate is the rate in effect when the report is made; nil
	// when no rate is known for the scope.
	LatestUSDRate *Rate
}

// StockItem is one Variant on the stocktaking sheet, valued in EGP.
type StockItem struct {
	VariantID      string
	SKU            string
	ProductID      string
	ProductNameAr  string
	ProductNameEn  string
	VariantNameAr  string
	VariantNameEn  string
	CategoryID     string
	CategoryNameAr string
	CategoryNameEn string
	Active         bool
	Quantity       int64
	UnitCost       *int64
	UnitPrice      *int64
}

// Repository reads report facts. An empty storeID means every Store;
// asOf is the moment the latest USD rate is read for.
type Repository interface {
	SalesReportFacts(ctx context.Context, storeID string, startUTC, endUTC, asOf time.Time, withUSDRates bool) (Facts, error)
	StockItems(ctx context.Context, storeID string) ([]StockItem, *Shop, error)
}
