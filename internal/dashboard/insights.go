package dashboard

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// Sync page reads: what arrived in the selected period, one sale with its
// costs, and the product image store. storeID "" reads every Store; the HTTP
// boundary has already authorized the scope.

// SyncCategories are the kinds the sync summary reports, in display order.
var SyncCategories = []string{"sales", "returns", "catalog", "stock"}

// SyncCategory is one kind of event: how many arrived in the period and when
// the newest one arrived at any time ("" when never).
type SyncCategory struct {
	Category       string `json:"category"`
	Synced         int64  `json:"synced"`
	LastReceivedAt string `json:"last_received_at,omitempty"`
}

// SyncSummary is the period view of synchronized events.
type SyncSummary struct {
	Categories     []SyncCategory `json:"categories"`
	TotalSynced    int64          `json:"total_synced"`
	LastReceivedAt string         `json:"last_received_at,omitempty"`
}

// SaleDetail is one finalized sale with its lines. Money is exact
// minor-unit strings in the sale currency.
type SaleDetail struct {
	SaleID        string           `json:"sale_id"`
	SaleNumber    string           `json:"sale_number"`
	Channel       string           `json:"channel"`
	OccurredAt    string           `json:"occurred_at"`
	Currency      string           `json:"currency"`
	SubtotalMinor string           `json:"subtotal_minor"`
	DiscountMinor string           `json:"discount_minor"`
	TaxMinor      string           `json:"tax_minor"`
	TotalMinor    string           `json:"total_minor"`
	CashierName   *string          `json:"cashier_name,omitempty"`
	FxMicrorate   *string          `json:"fx_rate_microrate,omitempty"`
	ShopNameAR    string           `json:"shop_name_ar"`
	ShopNameEN    string           `json:"shop_name_en"`
	Lines         []SaleDetailLine `json:"lines"`
	// CostMinor sums the known line costs; CostComplete is false when a
	// line has no recorded cost, so the profit is not the full sale.
	CostMinor    string `json:"cost_minor"`
	CostComplete bool   `json:"cost_complete"`
}

// SaleDetailLine is one sold item: the price it sold for and the unit cost
// Retail recorded at sale time (nil when unknown).
type SaleDetailLine struct {
	SKU            string  `json:"sku"`
	ProductName    string  `json:"product_name"`
	WidthCM        *int    `json:"width_cm,omitempty"`
	HeightCM       *int    `json:"height_cm,omitempty"`
	Quantity       int     `json:"quantity"`
	UnitPriceMinor string  `json:"unit_price_minor"`
	LineTotalMinor string  `json:"line_total_minor"`
	UnitCostMinor  *string `json:"unit_cost_minor"`
	LineCostMinor  *string `json:"line_cost_minor"`
}

// SaleDetailLineRow is the adapter row; money stays int64 until mapped.
type SaleDetailLineRow struct {
	SKU            string
	ProductName    string
	WidthCM        *int
	HeightCM       *int
	Quantity       int
	UnitPriceMinor int64
	LineTotalMinor int64
	UnitCostMinor  *int64
}

// SaleDetailRow is the adapter header row.
type SaleDetailRow struct {
	SaleID, SaleNumber, Channel, Currency string
	OccurredAt                            time.Time
	Subtotal, Discount, Tax, Total        int64
	CashierName                           *string
	FxMicrorate                           *int64
	ShopNameAR, ShopNameEN                string
}

// MediaStatus reports the product image store: whether it answers, and the
// files Cloud holds for the selected scope.
type MediaStatus struct {
	// Storage is "r2" (Cloudflare R2) or "local" (development only).
	Storage string `json:"storage"`
	// Check is "ok" (the newest file was found), "missing" (it was not),
	// "error" (the store did not answer) or "empty" (no file to check).
	Check           string `json:"check"`
	CheckedAt       string `json:"checked_at"`
	CheckMillis     int64  `json:"check_ms"`
	StoredFiles     int64  `json:"stored_files"`
	StoredBytes     int64  `json:"stored_bytes"`
	UploadedInRange int64  `json:"uploaded_in_range"`
	LastUploadAt    string `json:"last_upload_at,omitempty"`
	GalleryImages   int64  `json:"gallery_images"`
	WaitingImages   int64  `json:"waiting_images"`
}

// MediaStatusRow is the adapter row for MediaStatus counts.
type MediaStatusRow struct {
	StoredFiles, StoredBytes, UploadedInRange int64
	LastUploadAt                              *time.Time
	LatestSHA256                              string
	GalleryImages, WaitingImages              int64
}

// SyncCategoryRow is the adapter row for one event kind.
type SyncCategoryRow struct {
	Category       string
	Synced         int64
	LastReceivedAt *time.Time
}

// InsightsRepository reads the sync page data.
type InsightsRepository interface {
	DashboardSyncSummary(ctx context.Context, storeID string, startUTC, endUTC time.Time) ([]SyncCategoryRow, error)
	DashboardSaleDetail(ctx context.Context, storeID, saleID string) (SaleDetailRow, []SaleDetailLineRow, error)
	DashboardMediaStatus(ctx context.Context, storeID string, startUTC, endUTC time.Time) (MediaStatusRow, error)
}

// MediaProbe reports whether the object store holds one file.
type MediaProbe func(ctx context.Context, sha256 string) (bool, error)

// Insights serves the sync page reads.
type Insights struct {
	repo    InsightsRepository
	storage string
	probe   MediaProbe
	now     func() time.Time
}

// NewInsights builds the sync page reads. storage names the configured
// image store; probe checks one stored object without reading its bytes.
func NewInsights(repo InsightsRepository, storage string, probe MediaProbe, now func() time.Time) Insights {
	return Insights{repo: repo, storage: storage, probe: probe, now: now}
}

func scopeID(req report.Request) string {
	if req.Store == nil {
		return ""
	}
	return req.Store.StoreID
}

// SyncSummary counts events received in the request period per kind.
func (s Insights) SyncSummary(ctx context.Context, req report.Request) (SyncSummary, error) {
	rows, err := s.repo.DashboardSyncSummary(ctx, scopeID(req), req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return SyncSummary{}, err
	}
	byCategory := make(map[string]SyncCategoryRow, len(rows))
	for _, row := range rows {
		byCategory[row.Category] = row
	}
	out := SyncSummary{Categories: make([]SyncCategory, 0, len(SyncCategories))}
	var newest time.Time
	for _, category := range SyncCategories {
		row := byCategory[category]
		item := SyncCategory{Category: category, Synced: row.Synced}
		if row.LastReceivedAt != nil {
			item.LastReceivedAt = row.LastReceivedAt.UTC().Format(time.RFC3339)
			if row.LastReceivedAt.After(newest) {
				newest = *row.LastReceivedAt
			}
		}
		out.TotalSynced += row.Synced
		out.Categories = append(out.Categories, item)
	}
	if !newest.IsZero() {
		out.LastReceivedAt = newest.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// SaleDetail returns one sale of the scope with its lines and known costs.
func (s Insights) SaleDetail(ctx context.Context, req report.Request, saleID string) (SaleDetail, error) {
	if _, err := uuid.Parse(strings.TrimSpace(saleID)); err != nil {
		return SaleDetail{}, apperr.New(apperr.InvalidInput, "invalid sale id")
	}
	head, lines, err := s.repo.DashboardSaleDetail(ctx, scopeID(req), strings.TrimSpace(saleID))
	if err != nil {
		return SaleDetail{}, err
	}
	out := SaleDetail{
		SaleID: head.SaleID, SaleNumber: head.SaleNumber, Channel: head.Channel,
		OccurredAt: head.OccurredAt.UTC().Format(time.RFC3339), Currency: head.Currency,
		SubtotalMinor: minorString(head.Subtotal), DiscountMinor: minorString(head.Discount),
		TaxMinor: minorString(head.Tax), TotalMinor: minorString(head.Total),
		CashierName: head.CashierName, ShopNameAR: head.ShopNameAR, ShopNameEN: head.ShopNameEN,
		Lines: make([]SaleDetailLine, 0, len(lines)), CostComplete: true,
	}
	if head.FxMicrorate != nil {
		out.FxMicrorate = ptrStr(minorString(*head.FxMicrorate))
	}
	var cost int64
	for _, line := range lines {
		item := SaleDetailLine{
			SKU: line.SKU, ProductName: line.ProductName, WidthCM: line.WidthCM, HeightCM: line.HeightCM,
			Quantity: line.Quantity, UnitPriceMinor: minorString(line.UnitPriceMinor),
			LineTotalMinor: minorString(line.LineTotalMinor),
		}
		if line.UnitCostMinor == nil {
			out.CostComplete = false
		} else {
			lineCost, err := mulCheckedI64(*line.UnitCostMinor, int64(line.Quantity))
			if err != nil {
				return SaleDetail{}, err
			}
			if cost, err = addCheckedI64(cost, lineCost); err != nil {
				return SaleDetail{}, err
			}
			item.UnitCostMinor = ptrStr(minorString(*line.UnitCostMinor))
			item.LineCostMinor = ptrStr(minorString(lineCost))
		}
		out.Lines = append(out.Lines, item)
	}
	out.CostMinor = minorString(cost)
	return out, nil
}

// MediaStatus checks the image store with the newest file of the scope and
// reports the file counts.
func (s Insights) MediaStatus(ctx context.Context, req report.Request) (MediaStatus, error) {
	row, err := s.repo.DashboardMediaStatus(ctx, scopeID(req), req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return MediaStatus{}, err
	}
	out := MediaStatus{
		Storage: s.storage, StoredFiles: row.StoredFiles, StoredBytes: row.StoredBytes,
		UploadedInRange: row.UploadedInRange, GalleryImages: row.GalleryImages, WaitingImages: row.WaitingImages,
	}
	if row.LastUploadAt != nil {
		out.LastUploadAt = row.LastUploadAt.UTC().Format(time.RFC3339)
	}
	started := s.now()
	switch {
	case row.LatestSHA256 == "" || s.probe == nil:
		out.Check = "empty"
	default:
		found, err := s.probe(ctx, row.LatestSHA256)
		switch {
		case err != nil:
			out.Check = "error"
		case found:
			out.Check = "ok"
		default:
			out.Check = "missing"
		}
	}
	finished := s.now()
	out.CheckedAt = finished.UTC().Format(time.RFC3339)
	out.CheckMillis = finished.Sub(started).Milliseconds()
	return out, nil
}

// mulCheckedI64 multiplies non-negative amounts with an explicit overflow
// failure.
func mulCheckedI64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if a < 0 || b < 0 || product/b != a {
		return 0, apperr.New(apperr.Internal, "cost arithmetic overflow")
	}
	return product, nil
}
