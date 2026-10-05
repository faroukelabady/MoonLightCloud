package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
)

// Catalog admin persistence on Devices (shared pool + timeouts).
// Commands and targets are immutable except status transitions;
// history is never deleted.

func catalogAdminCommandToDomain(row sqlcgen.CatalogAdminCommand) catalogadmin.CommandView {
	return catalogadmin.CommandView{
		ID: row.ID.String(), StoreID: uuidString(row.StoreID),
		Type: row.CommandType, Version: int(row.CommandVersion),
		EntityID: row.EntityID, PayloadHash: row.PayloadHash,
		ExpectedRevision: row.ExpectedRevision, Actor: row.Actor,
		Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

// CreateCatalogAdminCommand persists one immutable command.
func (d Devices) CreateCatalogAdminCommand(ctx context.Context, cmd catalogadmin.NewCommand) (catalogadmin.CommandView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	id, err := parseUUID(cmd.ID)
	if err != nil {
		return catalogadmin.CommandView{}, err
	}
	storeID, err := parseUUID(cmd.StoreID)
	if err != nil {
		return catalogadmin.CommandView{}, err
	}
	row, err := sqlcgen.New(d.pool).CreateCatalogAdminCommand(ctx, sqlcgen.CreateCatalogAdminCommandParams{
		ID: id, StoreID: storeID, CommandType: cmd.Type,
		CommandVersion: int32(cmd.Version), EntityID: cmd.EntityID,
		Payload: cmd.Payload, PayloadHash: cmd.PayloadHash,
		ExpectedRevision: cmd.ExpectedRevision, Actor: cmd.Actor,
	})
	if err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	return catalogadmin.CommandView{
		ID: uuidString(row.ID), StoreID: uuidString(row.StoreID),
		Type: row.CommandType, Version: int(row.CommandVersion),
		EntityID: row.EntityID, PayloadHash: row.PayloadHash,
		ExpectedRevision: row.ExpectedRevision, Actor: row.Actor,
		Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// CreateCatalogAdminTarget snapshots one eligible device target.
func (d Devices) CreateCatalogAdminTarget(ctx context.Context, commandID, targetID, deviceID string) (catalogadmin.TargetView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	cid, err := parseUUID(commandID)
	if err != nil {
		return catalogadmin.TargetView{}, err
	}
	tid, err := parseUUID(targetID)
	if err != nil {
		return catalogadmin.TargetView{}, err
	}
	duid, err := parseUUID(deviceID)
	if err != nil {
		return catalogadmin.TargetView{}, err
	}
	row, err := sqlcgen.New(d.pool).CreateCatalogAdminTarget(ctx, sqlcgen.CreateCatalogAdminTargetParams{
		ID: tid, CommandID: cid, DeviceID: duid,
	})
	if err != nil {
		return catalogadmin.TargetView{}, catalogAdminErr(err)
	}
	return catalogAdminTargetToDomain(row), nil
}

// GetCatalogAdminCommand reads one command by ID.
func (d Devices) GetCatalogAdminCommand(ctx context.Context, id string) (catalogadmin.CommandView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalogadmin.CommandView{}, err
	}
	row, err := sqlcgen.New(d.pool).GetCatalogAdminCommand(ctx, uid)
	if err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	return catalogadmin.CommandView{
		ID: uuidString(row.ID), StoreID: uuidString(row.StoreID),
		Type: row.CommandType, Version: int(row.CommandVersion),
		EntityID: row.EntityID, Payload: row.Payload, PayloadHash: row.PayloadHash,
		ExpectedRevision: row.ExpectedRevision, Actor: row.Actor,
		Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// ListCatalogAdminCommands pages Store-scoped history (newest first).
func (d Devices) ListCatalogAdminCommands(ctx context.Context, storeID, commandType, entityID, status string, limit, offset int) ([]catalogadmin.CommandView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ListCatalogAdminCommands(ctx, sqlcgen.ListCatalogAdminCommandsParams{
		StoreID: suid, CommandType: commandType, EntityID: entityID, Status: status,
		LimitN: int32(limit), OffsetN: int32(offset),
	})
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.CommandView, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.CommandView{
			ID: uuidString(row.ID), StoreID: uuidString(row.StoreID),
			Type: row.CommandType, Version: int(row.CommandVersion),
			EntityID: row.EntityID, PayloadHash: row.PayloadHash,
			ExpectedRevision: row.ExpectedRevision, Actor: row.Actor,
			Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return out, nil
}

// ListCatalogAdminTargets reads all targets of one command.
func (d Devices) ListCatalogAdminTargets(ctx context.Context, commandID string) ([]catalogadmin.TargetView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	cid, err := parseUUID(commandID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ListCatalogAdminTargets(ctx, cid)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.TargetView, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.TargetView{
			ID: uuidString(row.ID), CommandID: uuidString(row.CommandID),
			DeviceID: uuidString(row.DeviceID), Status: row.Status,
			ResultCode: adminTextOrEmpty(row.ResultCode), EntityID: row.EntityID,
			PreRevision: row.PreRevision, PostRevision: row.PostRevision,
		})
	}
	return out, nil
}

// ListCatalogAdminTargetsBatch reads targets for many commands in
// one query (history lists must not fan out per command).
func (d Devices) ListCatalogAdminTargetsBatch(ctx context.Context, commandIDs []string) (map[string][]catalogadmin.TargetView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	ids := make([]pgtype.UUID, 0, len(commandIDs))
	for _, id := range commandIDs {
		uid, err := parseUUID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uid)
	}
	rows, err := sqlcgen.New(d.pool).ListCatalogAdminTargetsBatch(ctx, ids)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := map[string][]catalogadmin.TargetView{}
	for _, row := range rows {
		view := catalogAdminTargetToDomain(row)
		out[view.CommandID] = append(out[view.CommandID], view)
	}
	return out, nil
}

// DueCatalogAdminTargets lists deliverable targets for one device.
func (d Devices) DueCatalogAdminTargets(ctx context.Context, deviceID string, limit int) ([]catalogadmin.DueTarget, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DueCatalogAdminTargets(ctx, sqlcgen.DueCatalogAdminTargetsParams{
		DeviceID: duid, LimitN: int32(limit),
	})
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.DueTarget, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.DueTarget{
			TargetID: uuidString(row.ID), CommandID: uuidString(row.CommandID),
			DeviceID: uuidString(row.DeviceID), Status: row.Status,
			Type: row.CommandType, Version: int(row.CommandVersion),
			StoreID: uuidString(row.StoreID), EntityID: row.EntityID,
			Payload: row.Payload, PayloadHash: row.PayloadHash,
		})
	}
	return out, nil
}

// FinishCatalogAdminTarget records one device outcome. Only
// PENDING/DELIVERED rows move; replays of terminal rows are rejected
// by the query (no rows) so callers can treat them as idempotent.
func (d Devices) FinishCatalogAdminTarget(ctx context.Context, targetID, deviceID, status, code, entityID string, pre, post int64) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tid, err := parseUUID(targetID)
	if err != nil {
		return false, err
	}
	duid, err := parseUUID(deviceID)
	if err != nil {
		return false, err
	}
	_, err = sqlcgen.New(d.pool).FinishCatalogAdminTarget(ctx, sqlcgen.FinishCatalogAdminTargetParams{
		ID: tid, DeviceID: duid, Status: status, ResultCode: code,
		EntityID: entityID, PreRevision: pre, PostRevision: post,
	})
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, catalogAdminErr(err)
	}
	return true, nil
}

// MarkCatalogAdminTargetDelivered moves PENDING -> DELIVERED.
func (d Devices) MarkCatalogAdminTargetDelivered(ctx context.Context, targetID, deviceID string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tid, err := parseUUID(targetID)
	if err != nil {
		return err
	}
	duid, err := parseUUID(deviceID)
	if err != nil {
		return err
	}
	_, err = sqlcgen.New(d.pool).MarkCatalogAdminTargetDelivered(ctx, sqlcgen.MarkCatalogAdminTargetDeliveredParams{
		ID: tid, DeviceID: duid,
	})
	if err != nil && !isNotFoundErr(err) {
		return catalogAdminErr(err)
	}
	return nil
}

// CancelCatalogAdminCommand marks a PENDING command CANCELLED plus its
// pending targets. Applied targets are never rewritten.
func (d Devices) CancelCatalogAdminCommand(ctx context.Context, commandID string) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	cid, err := parseUUID(commandID)
	if err != nil {
		return false, err
	}
	if err := sqlcgen.New(d.pool).CancelPendingCatalogAdminTargets(ctx, cid); err != nil {
		return false, catalogAdminErr(err)
	}
	_, err = sqlcgen.New(d.pool).CancelCatalogAdminCommand(ctx, cid)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, catalogAdminErr(err)
	}
	return true, nil
}

// UpsertCatalogAdminCapability records Retail capability.
func (d Devices) UpsertCatalogAdminCapability(ctx context.Context, deviceID string, capable bool) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return err
	}
	_, err = sqlcgen.New(d.pool).UpsertCatalogAdminCapability(ctx, sqlcgen.UpsertCatalogAdminCapabilityParams{
		DeviceID: duid, Capable: capable,
	})
	return catalogAdminErr(err)
}

// GetCatalogAdminCapability reads Retail capability; missing means
// incapable (old Retail never announces).
func (d Devices) GetCatalogAdminCapability(ctx context.Context, deviceID string) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return false, err
	}
	row, err := sqlcgen.New(d.pool).GetCatalogAdminCapability(ctx, duid)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, catalogAdminErr(err)
	}
	return row.CatalogAdminCommandsV1, nil
}

// ListCatalogAdminBoundDevices snapshots eligible targets: every
// device currently bound to the Store.
func (d Devices) ListCatalogAdminBoundDevices(ctx context.Context, storeID string) ([]catalogadmin.BoundDevice, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ListStoreBoundDevices(ctx, suid)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.BoundDevice, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalogadmin.BoundDevice{DeviceID: uuidString(row.DeviceID), Name: row.Name})
	}
	return out, nil
}

// CatalogAdminOwnership verifies entity projection ownership and reads
// the convergence revision for the command stream.
func (d Devices) CatalogAdminOwnership(ctx context.Context, commandType, entityID string) (storeID string, revision int64, found bool, err error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(entityID)
	if err != nil {
		return "", 0, false, err
	}
	q := sqlcgen.New(d.pool)
	switch commandType {
	case catalogadmin.TypeProductDetailsUpdateV1, catalogadmin.TypeProductClassificationUpdateV1:
		row, err := q.AdminProductOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return storeOrEmpty(row.StoreID), row.SourceRevision, true, nil
	case catalogadmin.TypeProductOnlinePolicyUpdateV1:
		prod, err := q.AdminProductOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		pol, err := q.AdminProductPolicyRevision(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return storeOrEmpty(prod.StoreID), 0, true, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return uuidString(prod.StoreID), pol.SourceRevision, true, nil
	case catalogadmin.TypeProductConfigurationsUpdateV1:
		row, err := q.AdminProductConfigurationRevision(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		prod, err := q.AdminProductOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return uuidString(prod.StoreID), row.ConfigurationRevision, true, nil
	case catalogadmin.TypeCategoryDetailsUpdateV1, catalogadmin.TypeCategoryParentsUpdateV1,
		catalogadmin.TypeCategoryOnlinePolicyUpdateV1:
		row, err := q.AdminCategoryOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return storeOrEmpty(row.StoreID), row.SourceRevision, true, nil
	case catalogadmin.TypeTagDetailsUpdateV1:
		row, err := q.AdminTagOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return storeOrEmpty(row.StoreID), row.SourceRevision, true, nil
	default:
		return "", 0, false, nil
	}
}

func catalogAdminTargetToDomain(row sqlcgen.CatalogAdminCommandTarget) catalogadmin.TargetView {
	return catalogadmin.TargetView{
		ID: uuidString(row.ID), CommandID: uuidString(row.CommandID),
		DeviceID: uuidString(row.DeviceID), Status: row.Status,
		ResultCode: adminTextOrEmpty(row.ResultCode), EntityID: row.EntityID,
		PreRevision: row.PreRevision, PostRevision: row.PostRevision,
	}
}

func catalogAdminErr(err error) error {
	if err == nil {
		return nil
	}
	return reportErr("catalog admin", err)
}

func isNotFoundErr(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func adminTextOrEmpty(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

// storeOrEmpty renders a nullable projection store_id: legacy NULL
// rows yield "" so they can never match a specific Store scope.
func storeOrEmpty(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuidString(u)
}

// AdminProductList serves the operator list: Store-scoped, bounded
// search, keyset pagination. cursor "" starts from the newest.
func (d Devices) AdminProductList(ctx context.Context, storeID, search, cursor string, limit int) ([]catalogadmin.AdminProductRow, error) {
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
			ProductID: uuidString(row.ProductID), SKU: row.Sku,
			NameAR: row.NameAr, NameEN: ifaceString(row.NameEn),
			IsActive: row.IsActive, CatalogRevision: row.CatalogRevision,
			SellOnline: row.SellOnline, StockQuantity: row.StockQuantity,
			ConfigurationRevision: row.ConfigurationRevision,
			HasPending:            row.HasPending,
		})
	}
	return out, nil
}

// AdminProductDetail serves the full editor snapshot: current values,
// all revision streams, read-only stock/SKU. Money stays exact
// minor-unit strings (never floats).
func (d Devices) AdminProductDetail(ctx context.Context, storeID, productID string) (catalogadmin.AdminProductDetail, error) {
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
		ProductID: uuidString(row.ProductID), SKU: row.Sku,
		NameAR: row.NameAr, NameEN: ifaceString(row.NameEn),
		DescriptionAR: textOrEmpty(row.DescriptionAr), DescriptionEN: ifaceString(row.DescriptionEn),
		TopCategoryID: uuidString(row.TopCategoryID),
		IsActive:      row.IsActive, SellOnline: row.SellOnline, SellOffline: row.SellOffline,
		StockQuantity:   row.StockQuantity,
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
	if err == nil {
		detail.HasPending = pending
	}
	return detail, nil
}

// AdminCategoryList serves the Store-scoped tree rows.
func (d Devices) AdminCategoryList(ctx context.Context, storeID string) ([]catalogadmin.AdminCategoryRow, error) {
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
func (d Devices) AdminTagList(ctx context.Context, storeID string) ([]catalogadmin.AdminTagRow, error) {
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
			NameAR: textOrEmpty(row.NameAr), NameEN: textOrEmpty(row.NameEn),
			IsActive: row.IsActive, CatalogRevision: row.CatalogRevision,
			HasPending: row.HasPending,
		})
	}
	return out, nil
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
func (d Devices) AdminProductConfigurations(ctx context.Context, storeID, productID string) ([]catalogadmin.AdminConfigurationRow, error) {
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
