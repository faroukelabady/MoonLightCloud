package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

var _ dashboard.InsightsRepository = Reporting{}

// optionalStore maps "" (every Store) to SQL NULL.
func optionalStore(storeID string) (pgtype.UUID, error) {
	if storeID == "" {
		return pgtype.UUID{}, nil
	}
	return scopedStoreUUID(storeID)
}

func optionalTime(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

// DashboardSyncSummary counts received events per kind for the sync page.
func (d Reporting) DashboardSyncSummary(ctx context.Context, storeID string, startUTC, endUTC, recentStart, recentEnd time.Time) ([]dashboard.SyncCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := optionalStore(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardSyncSummary(ctx, sqlcgen.DashboardSyncSummaryParams{
		StartUtc:    pgtype.Timestamptz{Time: startUTC, Valid: true},
		EndUtc:      pgtype.Timestamptz{Time: endUTC, Valid: true},
		RecentStart: pgtype.Timestamptz{Time: recentStart, Valid: true},
		RecentEnd:   pgtype.Timestamptz{Time: recentEnd, Valid: true},
		StoreID:     suid,
	})
	if err != nil {
		return nil, reportErr("dashboard sync summary", err)
	}
	out := make([]dashboard.SyncCategoryRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, dashboard.SyncCategoryRow{Category: row.Category, Synced: row.Synced, ReceivedLast7Days: row.ReceivedLast7Days, TotalReceived: row.TotalReceived, LastReceivedAt: optionalTime(row.LastReceivedAt)})
	}
	return out, nil
}

// DashboardSaleDetail reads one sale of the scope and its lines. A sale of
// another Store is NotFound.
func (d Reporting) DashboardSaleDetail(ctx context.Context, storeID, saleID string) (dashboard.SaleDetailRow, []dashboard.SaleDetailLineRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := optionalStore(storeID)
	if err != nil {
		return dashboard.SaleDetailRow{}, nil, err
	}
	sid, err := parseUUID(saleID)
	if err != nil {
		return dashboard.SaleDetailRow{}, nil, apperr.New(apperr.InvalidInput, "invalid sale id")
	}
	q := sqlcgen.New(d.pool)
	head, err := q.DashboardSaleDetail(ctx, sqlcgen.DashboardSaleDetailParams{SaleID: sid, StoreID: suid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dashboard.SaleDetailRow{}, nil, apperr.New(apperr.NotFound, "sale not found")
		}
		return dashboard.SaleDetailRow{}, nil, reportErr("dashboard sale detail", err)
	}
	lines, err := q.DashboardSaleDetailLines(ctx, sid)
	if err != nil {
		return dashboard.SaleDetailRow{}, nil, reportErr("dashboard sale detail", err)
	}
	out := dashboard.SaleDetailRow{
		SaleID: uuidString(head.SaleID), SaleNumber: head.SaleNumber, Channel: head.Channel,
		Currency: head.Currency, OccurredAt: head.OccurredAt.Time,
		Subtotal: head.SubtotalMinor, Discount: head.DiscountMinor, Tax: head.TaxMinor, Total: head.TotalMinor,
		ShopNameAR: head.ShopNameAr, ShopNameEN: head.ShopNameEn,
	}
	if head.CashierName.Valid {
		name := head.CashierName.String
		out.CashierName = &name
	}
	if head.FxRateMicrorate.Valid {
		rate := head.FxRateMicrorate.Int64
		out.FxMicrorate = &rate
	}
	rows := make([]dashboard.SaleDetailLineRow, 0, len(lines))
	for _, line := range lines {
		item := dashboard.SaleDetailLineRow{
			SKU: line.Sku, ProductName: line.ProductName, Quantity: int(line.Quantity),
			UnitPriceMinor: line.UnitPriceMinor, LineTotalMinor: line.LineTotalMinor,
			CatalogPriceMinor: saleCatalogPrice(line.SaleCurrency, line.VariantPriceEgpMinor, line.VariantPriceUsdMinor), Attributes: saleAttributeSnapshot(line.VariantAttributes),
		}
		if line.WidthCm.Valid {
			w := int(line.WidthCm.Int32)
			item.WidthCM = &w
		}
		if line.HeightCm.Valid {
			h := int(line.HeightCm.Int32)
			item.HeightCM = &h
		}
		if line.CostMinor.Valid {
			cost := line.CostMinor.Int64
			item.UnitCostMinor = &cost
		}
		rows = append(rows, item)
	}
	return out, rows, nil
}

// DashboardMediaStatus counts stored product images and gallery entries.
func (d Reporting) DashboardMediaStatus(ctx context.Context, storeID string, startUTC, endUTC time.Time) (dashboard.MediaStatusRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := optionalStore(storeID)
	if err != nil {
		return dashboard.MediaStatusRow{}, err
	}
	q := sqlcgen.New(d.pool)
	files, err := q.DashboardMediaStatus(ctx, sqlcgen.DashboardMediaStatusParams{
		StartUtc: pgtype.Timestamptz{Time: startUTC, Valid: true},
		EndUtc:   pgtype.Timestamptz{Time: endUTC, Valid: true},
		StoreID:  suid,
	})
	if err != nil {
		return dashboard.MediaStatusRow{}, reportErr("dashboard media status", err)
	}
	galleries, err := q.DashboardGalleryStatus(ctx, suid)
	if err != nil {
		return dashboard.MediaStatusRow{}, reportErr("dashboard media status", err)
	}
	return dashboard.MediaStatusRow{
		StoredFiles: files.StoredFiles, StoredBytes: files.StoredBytes, UploadedInRange: files.UploadedInRange,
		LastUploadAt: optionalTime(files.LastUploadAt), LatestSHA256: files.LatestSha256,
		GalleryImages: galleries.GalleryImages, WaitingImages: galleries.WaitingImages,
	}, nil
}
