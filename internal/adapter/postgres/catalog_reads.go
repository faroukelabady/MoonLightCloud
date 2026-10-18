package postgres

import (
	"context"
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5"
)

// Catalog read API for future phases (internal/catalog.Repository).

// CatalogOnlineConfiguredProductsForStore enumerates the online-
// configured products of exactly one proven Store through canonical
// projected state. Provider publication must use this (or a single
// authoritative product) rather than any global enumeration, so one
// Store's write can never sweep another Store's products.
func (d Devices) CatalogOnlineConfiguredProductsForStore(ctx context.Context, storeID string, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "store_id must be a UUID")
	}
	if limit <= 0 || limit > 1000 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be 1..1000")
	}
	rows, err := sqlcgen.New(d.pool).CatalogOnlineConfiguredProductsForStore(ctx, sqlcgen.CatalogOnlineConfiguredProductsForStoreParams{
		StoreID: suid, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "online products for store", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, uuidString(row.ProductID))
	}
	return out, nil
}

func catalogNotFound(what string) error {
	return apperr.New(apperr.NotFound, what+" not found")
}

// CatalogProduct assembles the full projected product aggregate.
func (d Devices) CatalogProduct(ctx context.Context, id string) (catalog.Product, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Product{}, catalogNotFound("product")
	}
	q := sqlcgen.New(d.pool)
	row, err := q.CatalogProductByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Product{}, catalogNotFound("product")
		}
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog product", redact(err))
	}
	prices, err := q.CatalogProductPrices(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog prices", redact(err))
	}
	subs, err := q.CatalogProductSubcategories(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog subcategories", redact(err))
	}
	tags, err := q.CatalogProductTags(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog tags", redact(err))
	}
	product := catalog.Product{
		ProductHeader: catalog.ProductHeader{
			ID: uuidString(row.ProductID), NameAR: row.NameAr, NameEN: optionalTextPtr(row.NameEn),
			IsActive: row.IsActive, Revision: row.SourceRevision,
			SourceEventID: uuidString(row.SourceEventID),
			StoreID:       storeString(row.StoreID),
		},
		TopCategoryID: uuidString(row.TopCategoryID),
		DescriptionAR: optionalTextPtr(row.DescriptionAr), DescriptionEN: optionalTextPtr(row.DescriptionEn),
	}
	if row.WidthCm.Valid {
		width := int(row.WidthCm.Int32)
		product.WidthCM = &width
	}
	if row.HeightCm.Valid {
		height := int(row.HeightCm.Int32)
		product.HeightCM = &height
	}
	for _, price := range prices {
		entry := catalog.Price{Currency: price.Currency, PriceCents: price.PriceMinor}
		if price.CostMinor.Valid {
			cost := price.CostMinor.Int64
			entry.CostCents = &cost
		}
		product.Prices = append(product.Prices, entry)
	}
	for _, sub := range subs {
		product.SubcategoryIDs = append(product.SubcategoryIDs, uuidString(sub))
	}
	for _, tag := range tags {
		product.TagIDs = append(product.TagIDs, uuidString(tag))
	}
	return product, nil
}

// CatalogActiveProducts lists active product headers.
func (d Devices) CatalogActiveProducts(ctx context.Context, limit int) ([]catalog.ProductHeader, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CatalogActiveProducts(ctx, int32(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog products", redact(err))
	}
	out := make([]catalog.ProductHeader, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.ProductHeader{
			ID: uuidString(row.ProductID), NameAR: row.NameAr, NameEN: optionalTextPtr(row.NameEn),
			IsActive: true, Revision: row.SourceRevision,
			SourceEventID: uuidString(row.SourceEventID),
		})
	}
	return out, nil
}

// CatalogCategory assembles the projected category with its parent set.
func (d Devices) CatalogCategory(ctx context.Context, id string) (catalog.Category, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Category{}, catalogNotFound("category")
	}
	q := sqlcgen.New(d.pool)
	row, err := q.CatalogCategoryByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Category{}, catalogNotFound("category")
		}
		return catalog.Category{}, apperr.Wrap(apperr.Internal, "catalog category", redact(err))
	}
	parents, err := q.CatalogCategoryParents(ctx, uid)
	if err != nil {
		return catalog.Category{}, apperr.Wrap(apperr.Internal, "catalog parents", redact(err))
	}
	category := catalog.Category{
		ID: uuidString(row.CategoryID), Status: row.Status, NameAR: row.NameAr,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.NameEn.Valid {
		name := row.NameEn.String
		category.NameEN = &name
	}
	for _, parent := range parents {
		category.ParentIDs = append(category.ParentIDs, uuidString(parent))
	}
	return category, nil
}

// CatalogCategoryEdges lists every projected DAG edge.
func (d Devices) CatalogCategoryEdges(ctx context.Context) ([]catalog.Edge, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).AllCatalogCategoryEdges(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog edges", redact(err))
	}
	out := make([]catalog.Edge, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.Edge{ParentID: uuidString(row.ParentID), ChildID: uuidString(row.ChildID)})
	}
	return out, nil
}

// CatalogTag returns the projected tag.
func (d Devices) CatalogTag(ctx context.Context, id string) (catalog.Tag, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Tag{}, catalogNotFound("tag")
	}
	row, err := sqlcgen.New(d.pool).CatalogTagByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Tag{}, catalogNotFound("tag")
		}
		return catalog.Tag{}, apperr.Wrap(apperr.Internal, "catalog tag", redact(err))
	}
	tag := catalog.Tag{
		ID: uuidString(row.TagID), Slug: row.Slug, IsActive: row.IsActive, NameAR: row.NameAr,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.NameEn.Valid {
		name := row.NameEn.String
		tag.NameEN = &name
	}
	return tag, nil
}

// CatalogProductSalesPolicy returns the projected channel/allocation
// configuration for one product.
func (d Devices) CatalogProductSalesPolicy(ctx context.Context, id string) (catalog.ProductSalesPolicy, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.ProductSalesPolicy{}, catalogNotFound("sales policy")
	}
	row, err := sqlcgen.New(d.pool).CatalogProductSalesPolicyByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ProductSalesPolicy{}, catalogNotFound("sales policy")
		}
		return catalog.ProductSalesPolicy{}, apperr.Wrap(apperr.Internal, "catalog sales policy", redact(err))
	}
	policy := catalog.ProductSalesPolicy{
		ProductID: uuidString(row.ProductID), SellOffline: row.SellOffline, SellOnline: row.SellOnline,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.OnlineAllocationLimit.Valid {
		limit := int(row.OnlineAllocationLimit.Int64)
		policy.OnlineAllocationLimit = &limit
	}
	return policy, nil
}

// CatalogProductAvailability derives provider-neutral ONLINE availability
// from current product, policy, and inventory rows in one read
// transaction, so the three inputs are mutually consistent. The read
// performs no writes.
func (d Devices) CatalogProductAvailability(ctx context.Context, id string) (catalog.ProductAvailability, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.ProductAvailability{}, catalogNotFound("availability")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.ProductAvailability{}, apperr.Wrap(apperr.Internal, "catalog availability", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := sqlcgen.New(tx).CatalogAvailabilityByProductID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ProductAvailability{ProductID: id}, nil
		}
		return catalog.ProductAvailability{}, apperr.Wrap(apperr.Internal, "catalog availability", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.ProductAvailability{}, apperr.Wrap(apperr.Internal, "catalog availability", redact(err))
	}
	availability := catalog.ProductAvailability{
		ProductID: uuidString(row.ProductID), ProductActive: row.ProductActive,
		MissingPolicy: !row.SellOnline.Valid, MissingInventory: !row.StockQuantity.Valid,
	}
	if row.SellOnline.Valid {
		availability.SellOnline = row.SellOnline.Bool
	}
	if row.OnlineAllocationLimit.Valid {
		limit := int(row.OnlineAllocationLimit.Int64)
		availability.OnlineAllocationLimit = &limit
	}
	var stock int
	inventoryFound := row.StockQuantity.Valid
	if inventoryFound {
		stock = int(row.StockQuantity.Int64)
		quantity := stock
		availability.StockQuantity = &quantity

	}
	online, ready := catalog.ComputeProductAvailability(
		row.ProductActive, true,
		availability.SellOnline, !availability.MissingPolicy, availability.OnlineAllocationLimit,
		stock, inventoryFound,
	)
	availability.OnlineAvailable = online
	availability.Ready = ready
	return availability, nil
}

// CatalogProductOnlinePolicy reads the canonical effective-online policy
// for one Product (Phase 13). Read-only: no provider calls, no writes,
// no barrier interaction — durable Cloud state only.
func (d Devices) CatalogProductOnlinePolicy(ctx context.Context, id string) (catalog.ProductOnlinePolicy, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.ProductOnlinePolicy{}, err
	}
	row, err := sqlcgen.New(d.pool).CatalogProductOnlineState(ctx, uid)
	if err != nil {
		return catalog.ProductOnlinePolicy{}, reportErr("catalog product online policy", err)
	}
	policy := catalog.ProductOnlinePolicy{
		ProductID:          uuidString(row.ProductID),
		StoreID:            uuidPtr(row.StoreID),
		Allowed:            row.CategoryAllowsOnline.Bool,
		Reason:             catalog.OnlineAllowed,
		BlockingCategoryID: uuidPtr(row.BlockingCategoryID),
		PolicyFingerprint:  string(row.PolicyFingerprint),
		PolicyVersion:      string(row.PolicyVersion),
	}
	if !row.CategoryAllowsOnline.Bool {
		policy.Reason = row.BlockReason
		if policy.Reason == "" {
			policy.Reason = catalog.OnlineBlockedCategory
		}
	}
	return policy, nil
}

// CatalogProductConfigurations reads one Product's projected ONLINE
// configurations (Phase 15). Read-only durable Cloud state.
func (d Devices) CatalogProductConfigurations(ctx context.Context, id string) ([]catalog.ProductConfiguration, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).CatalogProductConfigurationsByProduct(ctx, uid)
	if err != nil {
		return nil, reportErr("catalog product configurations", err)
	}
	out := make([]catalog.ProductConfiguration, 0, len(rows))
	for _, row := range rows {
		entry := catalog.ProductConfiguration{
			ID: uuidString(row.ConfigurationID), Kind: row.Kind,
			StyleCode: row.StyleCode, StyleNameAR: row.StyleNameAr,
			ColorCode: row.ColorCode, ColorNameAR: row.ColorNameAr,
			PriceDeltaEGPCents: row.PriceDeltaEgpMinor,
			Enabled:            row.Enabled, Position: int(row.Position), Revision: row.ConfigurationRevision,
		}
		if row.StyleNameEn.Valid {
			value := row.StyleNameEn.String
			entry.StyleNameEN = &value
		}
		if row.ColorNameEn.Valid {
			value := row.ColorNameEn.String
			entry.ColorNameEN = &value
		}
		if row.PriceDeltaUsdMinor.Valid {
			value := row.PriceDeltaUsdMinor.Int64
			entry.PriceDeltaUSDCents = &value
		}
		out = append(out, entry)
	}
	return out, nil
}

// CatalogProductVariants returns the projected variants of one product
// with attributes and last-known stock. Tombstoned variants are excluded
// (historical rows stay queryable through admin/health surfaces).
func (d Devices) CatalogProductVariants(ctx context.Context, productID string) ([]catalog.Variant, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	puid, err := parseUUID(productID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	q := sqlcgen.New(d.pool)
	rows, err := q.CatalogProductVariantsByProduct(ctx, puid)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog variants", redact(err))
	}
	out := make([]catalog.Variant, 0, len(rows))
	for _, row := range rows {
		if row.Deleted {
			continue
		}
		variant := catalog.Variant{
			VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
			SKU: row.Sku, IsActive: row.IsActive,
			Position: int(row.Position), CombinationKey: row.CombinationKey,
			VariantRevision: row.VariantRevision, CatalogRevision: row.CatalogRevision,
			SourceEventID: uuidString(row.SourceEventID), Attributes: []catalog.VariantAttribute{},
		}
		if row.PriceEgpCents.Valid {
			price := row.PriceEgpCents.Int64
			variant.PriceEGPCents = &price
		}
		if row.PriceUsdCents.Valid {
			price := row.PriceUsdCents.Int64
			variant.PriceUSDCents = &price
		}
		attrs, err := q.CatalogProductVariantAttributes(ctx, row.VariantID)
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, "catalog variant attributes", redact(err))
		}
		for _, attr := range attrs {
			entry := catalog.VariantAttribute{
				DefinitionCode: attr.DefinitionCode, ValueCode: attr.ValueCode,
				NameAR: attr.NameAr, DefinitionNameAR: attr.DefinitionNameAr,
				Position: int(attr.Position),
			}
			if attr.NameEn.Valid {
				name := attr.NameEn.String
				entry.NameEN = &name
			}
			if attr.DefinitionNameEn.Valid {
				name := attr.DefinitionNameEn.String
				entry.DefinitionNameEN = &name
			}
			variant.Attributes = append(variant.Attributes, entry)
		}
		if inv, err := q.CatalogProductVariantInventoryByID(ctx, row.VariantID); err == nil {
			stock := int(inv.StockQuantity)
			variant.StockQuantity = &stock
			variant.InventoryRevision = inv.SourceRevision
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.Wrap(apperr.Internal, "catalog variant inventory", redact(err))
		}
		out = append(out, variant)
	}
	return out, nil
}

// CatalogProductVariantInventory returns the projected last-known
// variant inventory with its policy/catalog mirrors.
func (d Devices) CatalogProductVariantInventory(ctx context.Context, id string) (catalog.VariantInventory, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	vuid, err := parseUUID(id)
	if err != nil {
		return catalog.VariantInventory{}, catalogNotFound("variant inventory")
	}
	row, err := sqlcgen.New(d.pool).CatalogProductVariantInventoryByID(ctx, vuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.VariantInventory{}, catalogNotFound("variant inventory")
		}
		return catalog.VariantInventory{}, apperr.Wrap(apperr.Internal, "catalog variant inventory", redact(err))
	}
	out := catalog.VariantInventory{
		VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
		SKU: row.Sku, StockQuantity: int(row.StockQuantity), Revision: row.SourceRevision,
		Ready: row.Ready, SellOnline: row.SellOnline,
		PolicyRevision: row.PolicyRevision, CatalogRevision: row.CatalogRevision,
		SourceEventID:    uuidString(row.SourceEventID),
		SourceReceivedAt: row.SourceReceivedAt.Time, ProjectedAt: row.ProjectedAt.Time,
	}
	if row.OnlineAllocationLimit.Valid {
		limit := int(row.OnlineAllocationLimit.Int64)
		out.OnlineAllocationLimit = &limit
	}
	return out, nil
}

// CatalogProductVariantAvailability derives per-variant provider-neutral
// ONLINE availability from current variant, product, policy, and variant
// inventory rows in one read transaction (no writes). The variant-level
// policy mirror (sell_online, online_allocation_limit) carried by the
// inventory stream tightens the product-level policy when present.
func (d Devices) CatalogProductVariantAvailability(ctx context.Context, id string) (catalog.VariantAvailability, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	vuid, err := parseUUID(id)
	if err != nil {
		return catalog.VariantAvailability{}, catalogNotFound("availability")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.VariantAvailability{}, apperr.Wrap(apperr.Internal, "catalog variant availability", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := sqlcgen.New(tx).CatalogVariantAvailabilityByVariantID(ctx, vuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.VariantAvailability{VariantID: id, MissingVariant: true}, nil
		}
		return catalog.VariantAvailability{}, apperr.Wrap(apperr.Internal, "catalog variant availability", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.VariantAvailability{}, apperr.Wrap(apperr.Internal, "catalog variant availability", redact(err))
	}
	availability := catalog.VariantAvailability{
		VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
		VariantActive:  row.VariantActive && !row.VariantDeleted,
		MissingProduct: !row.ProductActive.Valid, MissingPolicy: !row.SellOnline.Valid,
		MissingInventory: !row.StockQuantity.Valid || !row.VariantReady.Valid || !row.VariantReady.Bool,
	}
	productActive := row.ProductActive.Valid && row.ProductActive.Bool
	sellOnline := row.SellOnline.Valid && row.SellOnline.Bool
	if availability.MissingPolicy {
		sellOnline = false
	}
	// Effective variant channel policy: the variant inventory mirror
	// tightens (never widens) the product policy.
	if row.VariantSellOnline.Valid && !row.VariantSellOnline.Bool {
		sellOnline = false
	}
	availability.ProductActive = productActive
	availability.SellOnline = sellOnline
	limit := row.OnlineAllocationLimit
	if row.VariantAllocationLimit.Valid {
		limit = row.VariantAllocationLimit
	}
	if limit.Valid {
		cap := int(limit.Int64)
		availability.OnlineAllocationLimit = &cap
	}
	var stock int
	inventoryFound := row.StockQuantity.Valid
	if inventoryFound {
		stock = int(row.StockQuantity.Int64)
		quantity := stock
		availability.StockQuantity = &quantity
		availability.InventoryRevision = row.InventoryRevision.Int64
		availability.InventoryEventID = uuidString(row.InventoryEventID)
		if row.InventoryProjectedAt.Valid {
			projected := row.InventoryProjectedAt.Time
			availability.InventoryProjectedAt = &projected
		}
	}
	availability.OnlineAvailable = catalog.ComputeVariantAvailability(
		productActive && sellOnline && !availability.MissingInventory, availability.VariantActive,
		stock, availability.OnlineAllocationLimit,
	)
	availability.Ready = !availability.MissingProduct && !availability.MissingPolicy && !availability.MissingInventory
	return availability, nil
}
