package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
)

// AdminProductList serves the operator list: Store-scoped, bounded
// search, keyset pagination. cursor "" starts from the newest.
func (d CatalogAdmin) AdminProductList(ctx context.Context, storeID, search, cursor string, limit int) ([]catalogadmin.AdminProductRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := sqlcgen.New(d.pool).AdminProductList(ctx, sqlcgen.AdminProductListParams{
		StoreID: suid, Search: search, Cursor: cursor, LimitN: int32(limit),
	})
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.AdminProductRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.AdminProductRow{
			ProductID:     uuidString(row.ProductID),
			ProductTypeID: retailProductTypeID(uuidString(row.ProductTypeID), storeUUID(&storeID), row.SourceTypeID, row.SourceStoreID),
			NameAR:        row.NameAr, NameEN: ifaceString(row.NameEn),
			IsActive: row.IsActive, CatalogRevision: row.CatalogRevision,
			SellOnline: row.SellOnline, VariantCount: row.VariantCount,
			DerivedStock:          row.DerivedStock,
			ConfigurationRevision: row.ConfigurationRevision,
			HasPending:            row.HasPending,
			ThumbnailSHA256:       row.ThumbnailSha256,
		})
	}
	return out, nil
}

// AdminProductDetail serves the full editor snapshot: current values,
// all revision streams, and the derived variant rollup. Money stays
// exact minor-unit strings (never floats).
func (d CatalogAdmin) AdminProductDetail(ctx context.Context, storeID, productID string) (catalogadmin.AdminProductDetail, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return catalogadmin.AdminProductDetail{}, err
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return catalogadmin.AdminProductDetail{}, err
	}
	q := sqlcgen.New(d.pool)
	row, err := q.AdminProductDetail(ctx, sqlcgen.AdminProductDetailParams{
		ProductID: puid, StoreID: suid,
	})
	if err != nil {
		return catalogadmin.AdminProductDetail{}, catalogAdminErr(err)
	}
	prices, err := q.AdminProductPrices(ctx, puid)
	if err != nil {
		return catalogadmin.AdminProductDetail{}, catalogAdminErr(err)
	}
	subs, err := q.AdminProductSubcategories(ctx, puid)
	if err != nil {
		return catalogadmin.AdminProductDetail{}, catalogAdminErr(err)
	}
	tags, err := q.AdminProductTags(ctx, puid)
	if err != nil {
		return catalogadmin.AdminProductDetail{}, catalogAdminErr(err)
	}
	pol, err := q.AdminProductPolicyRevision(ctx, puid)
	if err != nil && !isNotFoundErr(err) {
		return catalogadmin.AdminProductDetail{}, catalogAdminErr(err)
	}
	detail := catalogadmin.AdminProductDetail{
		ProductID:     uuidString(row.ProductID),
		ProductTypeID: retailProductTypeID(uuidString(row.ProductTypeID), storeUUID(&storeID), row.SourceTypeID, row.SourceStoreID),
		NameAR:        row.NameAr, NameEN: ifaceString(row.NameEn),
		DescriptionAR: textOrEmpty(row.DescriptionAr), DescriptionEN: ifaceString(row.DescriptionEn),
		TopCategoryID: uuidString(row.TopCategoryID),
		IsActive:      row.IsActive, SellOnline: row.SellOnline, SellOffline: row.SellOffline,
		VariantCount: row.VariantCount, DerivedStock: row.DerivedStock,
		CatalogRevision: row.CatalogRevision, SalesPolicyRevision: pol.SourceRevision,
		ConfigurationRevision: row.ConfigurationRevision,
		EGPPriceMinor:         "0", CostMinor: "0",
		SubcategoryIDs: []string{}, TagIDs: []string{},
	}
	if row.WidthCm.Valid {
		w := int(row.WidthCm.Int32)
		detail.WidthCM = &w
	}
	if row.HeightCm.Valid {
		h := int(row.HeightCm.Int32)
		detail.HeightCM = &h
	}
	for _, price := range prices {
		switch price.Currency {
		case "EGP":
			detail.EGPPriceMinor = pgIntToString(price.PriceMinor)
			if price.CostMinor.Valid {
				detail.CostMinor = pgIntToString(price.CostMinor.Int64)
			}
		case "USD":
			usd := pgIntToString(price.PriceMinor)
			detail.USDPriceMinor = &usd
		}
	}
	for _, id := range subs {
		detail.SubcategoryIDs = append(detail.SubcategoryIDs, uuidString(id))
	}
	for _, id := range tags {
		detail.TagIDs = append(detail.TagIDs, uuidString(id))
	}
	pending, err := q.AdminProductHasPending(ctx, sqlcgen.AdminProductHasPendingParams{
		StoreID: suid, ProductID: puid,
	})
	if err != nil {
		return catalogadmin.AdminProductDetail{}, catalogAdminErr(err)
	}
	detail.HasPending = pending
	media, err := q.CatalogProductMediaForCommerce(ctx, puid)
	if err != nil {
		return catalogadmin.AdminProductDetail{}, catalogAdminErr(err)
	}
	detail.Images = make([]catalogadmin.AdminProductImage, 0, len(media))
	for _, m := range media {
		detail.Images = append(detail.Images, catalogadmin.AdminProductImage{
			ImageID: uuidString(m.ImageID), VariantID: uuidString(m.VariantID),
			Position: int(m.Position), IsPrimary: m.IsPrimary, SHA256: m.Sha256,
			Width: int(m.Width), Height: int(m.Height), Available: m.Available,
		})
	}
	return detail, nil
}

// AdminMediaAllowed reports whether the operator UI may read one image: the
// file is stored and a gallery of one of the Store's Products names it.
func (d CatalogAdmin) AdminMediaAllowed(ctx context.Context, storeID, sha256 string) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return false, err
	}
	allowed, err := sqlcgen.New(d.pool).AdminStoreMediaAllowed(ctx, sqlcgen.AdminStoreMediaAllowedParams{
		Sha256: sha256, StoreID: suid,
	})
	if err != nil {
		return false, catalogAdminErr(err)
	}
	return allowed, nil
}

// AdminCategoryList serves the Store-scoped tree rows.
func (d CatalogAdmin) AdminCategoryList(ctx context.Context, storeID string) ([]catalogadmin.AdminCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).AdminCategoryList(ctx, suid)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.AdminCategoryRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.AdminCategoryRow{
			CategoryID: uuidString(row.CategoryID),
			NameAR:     row.NameAr, NameEN: textOrEmpty(row.NameEn),
			Status: row.Status, OnlineEnabled: row.OnlineEnabled,
			ParentIDs:       ifaceStringList(row.ParentIds),
			CatalogRevision: row.CatalogRevision, HasPending: row.HasPending,
		})
	}
	return out, nil
}

// AdminTagList serves the Store-scoped tag rows.
func (d CatalogAdmin) AdminTagList(ctx context.Context, storeID string) ([]catalogadmin.AdminTagRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).AdminTagList(ctx, suid)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.AdminTagRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.AdminTagRow{
			TagID: uuidString(row.TagID), Slug: row.Slug,
			NameAR: row.NameAr, NameEN: textOrEmpty(row.NameEn),
			IsActive: row.IsActive, CatalogRevision: row.CatalogRevision,
			HasPending: row.HasPending,
		})
	}
	return out, nil
}

func textOrEmptyPtr(v pgtype.Text) *string {
	if !v.Valid || v.String == "" {
		return nil
	}
	s := v.String
	return &s
}

func ifaceString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func ifaceStringList(v any) []string {
	if v == nil {
		return []string{}
	}
	switch list := v.(type) {
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return list
	}
	return []string{}
}

func textOrEmpty(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

// AdminProductConfigurations serves current frame options for the
// editor: IDs ride along so edits update rows instead of duplicating
// them (Phase 15 full-set semantics). Money stays exact strings.
func (d CatalogAdmin) AdminProductConfigurations(ctx context.Context, storeID, productID string) ([]catalogadmin.AdminConfigurationRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := parseUUID(storeID); err != nil {
		return nil, err
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).AdminProductConfigurations(ctx, puid)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.AdminConfigurationRow, 0, len(rows))
	for _, row := range rows {
		var usd *string
		if row.PriceDeltaUsdMinor.Valid {
			s := pgIntToString(row.PriceDeltaUsdMinor.Int64)
			usd = &s
		}
		var styleEN, colorEN *string
		if row.StyleNameEn.Valid {
			s := row.StyleNameEn.String
			styleEN = &s
		}
		if row.ColorNameEn.Valid {
			s := row.ColorNameEn.String
			colorEN = &s
		}
		out = append(out, catalogadmin.AdminConfigurationRow{
			ID:        uuidString(row.ConfigurationID),
			StyleCode: row.StyleCode, StyleNameAR: row.StyleNameAr, StyleNameEN: styleEN,
			ColorCode: row.ColorCode, ColorNameAR: row.ColorNameAr, ColorNameEN: colorEN,
			EGPDeltaMinor: pgIntToString(row.PriceDeltaEgpMinor), USDDeltaMinor: usd,
			Enabled: row.Enabled, Position: int(row.Position),
		})
	}
	return out, nil
}

// adminVariantRow flattens the identical variant row shape both admin
// variant queries return.
type adminVariantRow struct {
	VariantID         pgtype.UUID
	ProductID         pgtype.UUID
	Sku               string
	IsActive          bool
	Deleted           bool
	PriceEgpCents     pgtype.Int8
	PriceUsdCents     pgtype.Int8
	Position          int32
	CombinationKey    string
	VariantRevision   int64
	CatalogRevision   int64
	StockQuantity     int64
	InventoryRevision int64
	HasPending        bool
}

// adminVariantToDomain renders one variant row with exact minor-unit
// money strings (never floats).
func adminVariantToDomain(row adminVariantRow) catalogadmin.AdminProductVariant {
	entry := catalogadmin.AdminProductVariant{
		VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
		SKU: row.Sku, IsActive: row.IsActive, Deleted: row.Deleted,
		StockQuantity: row.StockQuantity, Position: int(row.Position),
		CombinationKey: row.CombinationKey, VariantRevision: row.VariantRevision,
		CatalogRevision: row.CatalogRevision, InventoryRevision: row.InventoryRevision,
		HasPending: row.HasPending, Attributes: []catalogadmin.AdminProductVariantAttribute{},
		PriceEGPMinor: "0",
	}
	if row.PriceEgpCents.Valid {
		entry.PriceEGPMinor = pgIntToString(row.PriceEgpCents.Int64)
	}
	if row.PriceUsdCents.Valid {
		usd := pgIntToString(row.PriceUsdCents.Int64)
		entry.PriceUSDMinor = &usd
	}
	return entry
}

// adminVariantAttributes loads the option attribute rows for one
// variant in canonical (position, definition_code) order.
func adminVariantAttributes(ctx context.Context, q *sqlcgen.Queries, vuid pgtype.UUID) ([]catalogadmin.AdminProductVariantAttribute, error) {
	rows, err := q.AdminProductVariantAttributes(ctx, vuid)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.AdminProductVariantAttribute, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.AdminProductVariantAttribute{
			DefinitionCode: row.DefinitionCode, ValueCode: row.ValueCode,
			NameAR: row.NameAr, NameEN: adminTextOrEmpty(row.NameEn),
			DefinitionNameAR: row.DefinitionNameAr,
			DefinitionNameEN: adminTextOrEmpty(row.DefinitionNameEn),
			Position:         int(row.Position),
		})
	}
	return out, nil
}

// AdminProductVariants serves one Product's projected variant rows for
// the operator UI (Store-scoped; tombstones are flagged, never hidden).
func (d CatalogAdmin) AdminProductVariants(ctx context.Context, storeID, productID string) ([]catalogadmin.AdminProductVariant, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, err
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return nil, err
	}
	q := sqlcgen.New(d.pool)
	rows, err := q.AdminProductVariants(ctx, sqlcgen.AdminProductVariantsParams{
		StoreID: suid, ProductID: puid,
	})
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.AdminProductVariant, 0, len(rows))
	for _, row := range rows {
		entry := adminVariantToDomain(adminVariantRow{
			VariantID: row.VariantID, ProductID: row.ProductID, Sku: row.Sku,
			IsActive: row.IsActive, Deleted: row.Deleted,
			PriceEgpCents: row.PriceEgpCents, PriceUsdCents: row.PriceUsdCents,
			Position: row.Position, CombinationKey: row.CombinationKey,
			VariantRevision: row.VariantRevision, CatalogRevision: row.CatalogRevision,
			StockQuantity: row.StockQuantity, InventoryRevision: row.InventoryRevision,
			HasPending: row.HasPending,
		})
		attrs, err := adminVariantAttributes(ctx, q, row.VariantID)
		if err != nil {
			return nil, err
		}
		entry.Attributes = attrs
		out = append(out, entry)
	}
	return out, nil
}

// AdminProductVariant serves one projected variant row by variant ID
// (Store-scoped; unknown/foreign rows are NotFound, never a fallback).
func (d CatalogAdmin) AdminProductVariant(ctx context.Context, storeID, variantID string) (catalogadmin.AdminProductVariant, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return catalogadmin.AdminProductVariant{}, err
	}
	vuid, err := parseUUID(variantID)
	if err != nil {
		return catalogadmin.AdminProductVariant{}, err
	}
	q := sqlcgen.New(d.pool)
	row, err := q.AdminProductVariantByVariantID(ctx, sqlcgen.AdminProductVariantByVariantIDParams{
		StoreID: suid, VariantID: vuid,
	})
	if err != nil {
		if isNotFoundErr(err) {
			return catalogadmin.AdminProductVariant{}, apperr.New(apperr.NotFound, catalogadmin.CodeEntityNotFound)
		}
		return catalogadmin.AdminProductVariant{}, catalogAdminErr(err)
	}
	entry := adminVariantToDomain(adminVariantRow{
		VariantID: row.VariantID, ProductID: row.ProductID, Sku: row.Sku,
		IsActive: row.IsActive, Deleted: row.Deleted,
		PriceEgpCents: row.PriceEgpCents, PriceUsdCents: row.PriceUsdCents,
		Position: row.Position, CombinationKey: row.CombinationKey,
		VariantRevision: row.VariantRevision, CatalogRevision: row.CatalogRevision,
		StockQuantity: row.StockQuantity, InventoryRevision: row.InventoryRevision,
		HasPending: row.HasPending,
	})
	attrs, err := adminVariantAttributes(ctx, q, vuid)
	if err != nil {
		return catalogadmin.AdminProductVariant{}, err
	}
	entry.Attributes = attrs
	return entry, nil
}

// AdminProductTypeList serves Store-scoped projected types for the editor.
func (d CatalogAdmin) AdminProductTypeList(ctx context.Context, storeID string) ([]catalogadmin.AdminProductType, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	q := sqlcgen.New(d.pool)
	rows, err := q.AdminProductTypeList(ctx, storeID)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.AdminProductType, 0, len(rows))
	for _, row := range rows {
		dims, err := q.CatalogProductTypeDimensions(ctx, row.TypeID)
		if err != nil {
			return nil, catalogAdminErr(err)
		}
		caps, err := q.CatalogProductTypeCapabilities(ctx, row.TypeID)
		if err != nil {
			return nil, catalogAdminErr(err)
		}
		if dims == nil {
			dims = []string{}
		}
		if caps == nil {
			caps = []string{}
		}
		out = append(out, catalogadmin.AdminProductType{
			TypeID: retailProductTypeID(uuidString(row.TypeID), storeUUID(&storeID), row.SourceTypeID, row.SourceStoreID), Code: row.Code,
			NameAR: row.NameAr, NameEN: row.NameEn,
			DescriptionAR: textOrEmptyPtr(row.DescriptionAr), DescriptionEN: textOrEmptyPtr(row.DescriptionEn),
			IsActive: row.IsActive, Position: int(row.Position),
			TypeRevision: row.TypeRevision,
			Dimensions:   dims, Capabilities: caps,
		})
	}
	return out, nil
}

// AdminProductType serves one projected type with its sets.
func (d CatalogAdmin) AdminProductType(ctx context.Context, storeID, typeID string) (catalogadmin.AdminProductType, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tuid, err := parseUUID(typeID)
	if err != nil {
		return catalogadmin.AdminProductType{}, err
	}
	q := sqlcgen.New(d.pool)
	projectedID, err := projectedProductTypeID(ctx, q, typeID, storeUUID(&storeID))
	if err != nil {
		return catalogadmin.AdminProductType{}, catalogAdminErr(err)
	}
	tuid, _ = parseUUID(projectedID)
	row, err := q.AdminProductTypeDetail(ctx, sqlcgen.AdminProductTypeDetailParams{TypeID: tuid, StoreID: storeID})
	if err != nil {
		return catalogadmin.AdminProductType{}, catalogAdminErr(err)
	}
	dims, err := q.CatalogProductTypeDimensions(ctx, tuid)
	if err != nil {
		return catalogadmin.AdminProductType{}, catalogAdminErr(err)
	}
	caps, err := q.CatalogProductTypeCapabilities(ctx, tuid)
	if err != nil {
		return catalogadmin.AdminProductType{}, catalogAdminErr(err)
	}
	if dims == nil {
		dims = []string{}
	}
	if caps == nil {
		caps = []string{}
	}
	return catalogadmin.AdminProductType{
		TypeID: retailProductTypeID(uuidString(row.TypeID), storeUUID(&storeID), row.SourceTypeID, row.SourceStoreID), Code: row.Code,
		NameAR: row.NameAr, NameEN: row.NameEn,
		DescriptionAR: textOrEmptyPtr(row.DescriptionAr), DescriptionEN: textOrEmptyPtr(row.DescriptionEn),
		IsActive: row.IsActive, Position: int(row.Position),
		TypeRevision: row.TypeRevision,
		Dimensions:   dims, Capabilities: caps,
	}, nil
}
