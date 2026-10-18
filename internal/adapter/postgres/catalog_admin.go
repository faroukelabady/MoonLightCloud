package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// Catalog admin persistence on Devices (shared pool + timeouts).
// Commands and targets are immutable except status transitions;
// history is never deleted.

// commandIdentity maps the Phase 17-R3 identity columns: TargetKind +
// RequestedKey + ResultEntityID. EntityID "" is the explicit absent
// marker for creates (never a placeholder UUID).
func commandIdentity(kind string, requested, result pgtype.Text) (string, string, string) {
	return kind, textOrEmpty(requested), textOrEmpty(result)
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
	kind := cmd.TargetKind
	if kind == "" {
		// Legacy/entity callers predate target kinds: default to entity.
		kind = catalogadmin.TargetKindEntity
	}
	row, err := sqlcgen.New(d.pool).CreateCatalogAdminCommand(ctx, sqlcgen.CreateCatalogAdminCommandParams{
		ID: id, StoreID: storeID, CommandType: cmd.Type,
		CommandVersion: int32(cmd.Version), EntityID: cmd.EntityID,
		TargetKind: kind, RequestedKey: requestedKeyParam(cmd.RequestedKey),
		Payload: cmd.Payload, PayloadHash: cmd.PayloadHash,
		ExpectedRevision: cmd.ExpectedRevision, Actor: cmd.Actor,
	})
	if err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	kind, requested, result := commandIdentity(row.TargetKind, row.RequestedKey, row.ResultEntityID)
	return catalogadmin.CommandView{
		ID: uuidString(row.ID), StoreID: uuidString(row.StoreID),
		Type: row.CommandType, Version: int(row.CommandVersion),
		EntityID: row.EntityID, TargetKind: kind, RequestedKey: requested, ResultEntityID: result,
		PayloadHash:      row.PayloadHash,
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
	kind, requested, result := commandIdentity(row.TargetKind, row.RequestedKey, row.ResultEntityID)
	return catalogadmin.CommandView{
		ID: uuidString(row.ID), StoreID: uuidString(row.StoreID),
		Type: row.CommandType, Version: int(row.CommandVersion),
		EntityID: row.EntityID, TargetKind: kind, RequestedKey: requested, ResultEntityID: result,
		Payload: row.Payload, PayloadHash: row.PayloadHash,
		ExpectedRevision: row.ExpectedRevision, Actor: row.Actor,
		Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// ListCatalogAdminCommands pages Store-scoped history newest-first with a
// keyset cursor: rows strictly older than (cursorTS, cursorID). A zero
// cursorTS starts from the newest row. Keyset pagination is stable under
// concurrent inserts: new commands never shift already-returned pages,
// so readers observe neither duplicates nor skips.
func (d Devices) ListCatalogAdminCommands(ctx context.Context, storeID, commandType, entityID, status string, limit int, cursorTS time.Time, cursorID string) ([]catalogadmin.CommandView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, err
	}
	var ts pgtype.Timestamptz
	var cid pgtype.UUID
	if !cursorTS.IsZero() {
		ts = pgtype.Timestamptz{Time: cursorTS.UTC(), Valid: true}
		parsed, err := parseUUID(cursorID)
		if err != nil {
			return nil, err
		}
		cid = parsed
	}
	rows, err := sqlcgen.New(d.pool).ListCatalogAdminCommands(ctx, sqlcgen.ListCatalogAdminCommandsParams{
		StoreID: suid, CommandType: commandType, EntityID: entityID, Status: status,
		LimitN: int32(limit), CursorTs: ts, CursorID: cid,
	})
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]catalogadmin.CommandView, 0, len(rows))
	for _, row := range rows {
		kind, requested, result := commandIdentity(row.TargetKind, row.RequestedKey, row.ResultEntityID)
		out = append(out, catalogadmin.CommandView{
			ID: uuidString(row.ID), StoreID: uuidString(row.StoreID),
			Type: row.CommandType, Version: int(row.CommandVersion),
			EntityID: row.EntityID, TargetKind: kind, RequestedKey: requested, ResultEntityID: result,
			PayloadHash:      row.PayloadHash,
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
// PENDING/DELIVERED rows move; identical terminal replays succeed,
// contradictory terminal outcomes conflict under the command lock.
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
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return false, catalogAdminErr(err)
	}
	defer tx.Rollback(ctx)
	// Lock command first in every ACK/cancel transaction, then device/binding/target.
	var commandID string
	if err = tx.QueryRow(ctx, `SELECT command_id::text FROM catalog_admin_command_targets WHERE id=$1 AND device_id=$2`, tid, duid).Scan(&commandID); err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, catalogAdminErr(err)
	}
	var cmd catalogadmin.CommandView
	if err = tx.QueryRow(ctx, `SELECT id::text,store_id::text,entity_id,target_kind,expected_revision,status,command_type,command_version,payload,COALESCE(result_entity_id,'') FROM catalog_admin_commands WHERE id=$1 FOR UPDATE`, commandID).Scan(&cmd.ID, &cmd.StoreID, &cmd.EntityID, &cmd.TargetKind, &cmd.ExpectedRevision, &cmd.Status, &cmd.Type, &cmd.Version, &cmd.Payload, &cmd.ResultEntityID); err != nil {
		return false, catalogAdminErr(err)
	}
	var currentStatus string
	var currentCode, currentEntity string
	var currentPre, currentPost int64
	if err = tx.QueryRow(ctx, `SELECT status,COALESCE(result_code,''),entity_id,pre_revision,post_revision FROM catalog_admin_command_targets WHERE id=$1 FOR UPDATE`, tid).Scan(&currentStatus, &currentCode, &currentEntity, &currentPre, &currentPost); err != nil {
		return false, catalogAdminErr(err)
	}
	// Server-owned lifecycle reconciliation is allowed only for genuinely invalid targets.
	if status == catalogadmin.TargetSkippedRevoked {
		var eligible bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices d JOIN device_store_bindings b ON b.device_id=d.id WHERE d.id=$1 AND d.status='active' AND b.store_id=$2)`, duid, cmd.StoreID).Scan(&eligible); err != nil {
			return false, catalogAdminErr(err)
		}
		if eligible {
			return false, nil
		}
	} else if status == catalogadmin.TargetBlockedCapability {
		if code != catalogadmin.CodeUnsupportedCommand || (catalogadmin.IsKnownType(cmd.Type) && cmd.Version == 1) {
			return false, apperr.New(apperr.InvalidInput, "invalid capability block")
		}
	} else {
		var lifecycle, bound string
		if err = tx.QueryRow(ctx, `SELECT status FROM devices WHERE id=$1 FOR SHARE`, duid).Scan(&lifecycle); err != nil {
			return false, catalogAdminErr(err)
		}
		if err = tx.QueryRow(ctx, `SELECT store_id::text FROM device_store_bindings WHERE device_id=$1 FOR SHARE`, duid).Scan(&bound); err != nil {
			if isNotFoundErr(err) {
				return false, nil
			}
			return false, catalogAdminErr(err)
		}
		if lifecycle != "active" || !strings.EqualFold(bound, cmd.StoreID) {
			return false, apperr.New(apperr.NotFound, "unknown command target")
		}
		if err = catalogadmin.ValidateOutcome(cmd, status, code, entityID, pre, post); err != nil {
			return false, apperr.New(apperr.InvalidInput, err.Error())
		}
	}
	persistResult := func() error {
		if status != catalogadmin.TargetApplied {
			return nil
		}
		if cmd.ResultEntityID != "" && !strings.EqualFold(cmd.ResultEntityID, entityID) {
			return apperr.New(apperr.Conflict, "contradictory command result identity")
		}
		_, err := sqlcgen.New(tx).SetCommandResultEntity(ctx, sqlcgen.SetCommandResultEntityParams{
			TargetID: tid, ResultEntityID: entityID,
		})
		return catalogAdminErr(err)
	}
	if currentStatus != catalogadmin.TargetPending && currentStatus != catalogadmin.TargetDelivered {
		if currentStatus == status && currentCode == code && currentEntity == entityID && currentPre == pre && currentPost == post {
			// A replay also repairs a pre-remediation APPLIED target whose
			// parent result identity was never durably recorded.
			if err := persistResult(); err != nil {
				return false, err
			}
			return true, tx.Commit(ctx)
		}
		return false, apperr.New(apperr.Conflict, "contradictory terminal outcome")
	}
	if cmd.Status != catalogadmin.CommandPending {
		return false, apperr.New(apperr.Conflict, "command cancelled")
	}
	_, err = sqlcgen.New(tx).FinishCatalogAdminTarget(ctx, sqlcgen.FinishCatalogAdminTargetParams{ID: tid, DeviceID: duid, Status: status, ResultCode: code, EntityID: entityID, PreRevision: pre, PostRevision: post})
	if err != nil {
		return false, catalogAdminErr(err)
	}
	if err = persistResult(); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
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
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalogAdminErr(err)
	}
	defer tx.Rollback(ctx)
	var cid string
	if err = tx.QueryRow(ctx, `SELECT command_id::text FROM catalog_admin_command_targets WHERE id=$1 AND device_id=$2`, tid, duid).Scan(&cid); err != nil {
		return catalogAdminErr(err)
	}
	var cmdStatus, sid string
	if err = tx.QueryRow(ctx, `SELECT status,store_id::text FROM catalog_admin_commands WHERE id=$1 FOR UPDATE`, cid).Scan(&cmdStatus, &sid); err != nil {
		return catalogAdminErr(err)
	}
	var lifecycle, bound string
	if err = tx.QueryRow(ctx, `SELECT status FROM devices WHERE id=$1 FOR SHARE`, duid).Scan(&lifecycle); err != nil {
		return catalogAdminErr(err)
	}
	if err = tx.QueryRow(ctx, `SELECT store_id::text FROM device_store_bindings WHERE device_id=$1 FOR SHARE`, duid).Scan(&bound); err != nil {
		return catalogAdminErr(err)
	}
	if cmdStatus != catalogadmin.CommandPending || lifecycle != "active" || bound != sid {
		return apperr.New(apperr.Conflict, "command no longer deliverable")
	}
	tag, err := tx.Exec(ctx, `UPDATE catalog_admin_command_targets SET status='delivered',delivered_at=COALESCE(delivered_at,now()),updated_at=now() WHERE id=$1 AND device_id=$2 AND status IN ('pending','delivered')`, tid, duid)
	if err != nil {
		return catalogAdminErr(err)
	}
	if tag.RowsAffected() != 1 {
		return apperr.New(apperr.Conflict, "command no longer deliverable")
	}
	return catalogAdminErr(tx.Commit(ctx))
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
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return false, catalogAdminErr(err)
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM catalog_admin_commands WHERE id=$1 FOR UPDATE`, cid).Scan(&status); err != nil {
		return false, catalogAdminErr(err)
	}
	if status != catalogadmin.CommandPending {
		return false, nil
	}
	var unsafe bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_admin_command_targets WHERE command_id=$1 AND status IN ('applied','delivered'))`, cid).Scan(&unsafe); err != nil {
		return false, catalogAdminErr(err)
	}
	// A delivered command may already be executing: cancellation cannot retract it safely.
	if unsafe {
		return false, apperr.New(apperr.Conflict, "command delivered or applied; issue a compensating command")
	}
	q := sqlcgen.New(tx)
	if err = q.CancelPendingCatalogAdminTargets(ctx, cid); err != nil {
		return false, catalogAdminErr(err)
	}
	if _, err = q.CancelCatalogAdminCommand(ctx, cid); err != nil {
		return false, catalogAdminErr(err)
	}
	if err = tx.Commit(ctx); err != nil {
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
	q := sqlcgen.New(d.pool)
	if capable {
		return catalogAdminErr(q.AddDeviceCapability(ctx, sqlcgen.AddDeviceCapabilityParams{
			DeviceID: duid, Capability: catalogadmin.CapabilityV1,
		}))
	}
	return catalogAdminErr(q.RemoveDeviceCapability(ctx, sqlcgen.RemoveDeviceCapabilityParams{
		DeviceID: duid, Capability: catalogadmin.CapabilityV1,
	}))
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
	capable, err := sqlcgen.New(d.pool).HasDeviceCapability(ctx, sqlcgen.HasDeviceCapabilityParams{
		DeviceID: duid, Capability: catalogadmin.CapabilityV1,
	})
	if err != nil {
		return false, catalogAdminErr(err)
	}
	return capable, nil
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
func (d Devices) CatalogAdminOwnership(ctx context.Context, commandType, entityID, requestedStoreID string) (storeID string, revision int64, found bool, err error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(entityID)
	if err != nil {
		return "", 0, false, err
	}
	q := sqlcgen.New(d.pool)
	switch commandType {
	case catalogadmin.TypeProductDetailsUpdateV1, catalogadmin.TypeProductClassificationUpdateV1,
		catalogadmin.TypeProductVariantsUpdateV1:
		row, err := q.AdminProductOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return storeOrEmpty(row.StoreID), row.SourceRevision, true, nil
	case catalogadmin.TypeProductTypeCreateV1, catalogadmin.TypeProductTypeDetailsUpdateV1,
		catalogadmin.TypeProductTypeDimensionsUpdateV1, catalogadmin.TypeProductTypeCapabilitiesUpdateV1,
		catalogadmin.TypeProductTypeStatusUpdateV1:
		// Type-scoped commands converge on the type stream (type_revision).
		// Creates evaluate on the RESULT identity (the aggregate layer
		// passes the Retail-minted ID, never the absent marker).
		scope := storeUUID(&requestedStoreID)
		projectedID, err := projectedProductTypeID(ctx, q, entityID, scope)
		if err != nil {
			return "", 0, false, catalogAdminErr(err)
		}
		uid, _ = parseUUID(projectedID)
		row, err := q.AdminProductTypeOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return storeOrEmpty(row.StoreID), row.TypeRevision, true, nil
	case catalogadmin.TypeProductTypeAssignV1:
		// Assignment versions on the Product aggregate stream: ownership
		// is the product's store.
		row, err := q.AdminProductOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return storeOrEmpty(row.StoreID), row.SourceRevision, true, nil
	case catalogadmin.TypeProductVariantUpdateV1, catalogadmin.TypeVariantAttributesUpdateV1:
		// Variant-scoped commands converge on the variant catalog
		// stream (variant_revision).
		row, err := q.AdminVariantOwnership(ctx, uid)
		if err != nil {
			if isNotFoundErr(err) {
				return "", 0, false, nil
			}
			return "", 0, false, catalogAdminErr(err)
		}
		return storeOrEmpty(row.StoreID), row.VariantRevision, true, nil
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
	if errors.Is(err, ErrProductTypeIdentityCollision) {
		return apperr.New(apperr.Conflict, "PRODUCT_TYPE_IDENTITY_CONFLICT")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.New(apperr.NotFound, catalogadmin.CodeEntityNotFound)
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

// CreateCatalogAdminCommandWithTargets atomically snapshots all currently bound
// devices with immutable intent. Any target insertion failure rolls back all.
func (d Devices) CreateCatalogAdminCommandWithTargets(ctx context.Context, cmd catalogadmin.NewCommand) (catalogadmin.CommandView, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	defer tx.Rollback(ctx)
	id, err := parseUUID(cmd.ID)
	if err != nil {
		return catalogadmin.CommandView{}, err
	}
	sid, err := parseUUID(cmd.StoreID)
	if err != nil {
		return catalogadmin.CommandView{}, err
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM stores WHERE id=$1 FOR UPDATE`, sid).Scan(&locked); err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	q := sqlcgen.New(tx)
	kind := cmd.TargetKind
	if kind == "" {
		kind = catalogadmin.TargetKindEntity
	}
	row, err := q.CreateCatalogAdminCommand(ctx, sqlcgen.CreateCatalogAdminCommandParams{ID: id, StoreID: sid, CommandType: cmd.Type, CommandVersion: int32(cmd.Version), EntityID: cmd.EntityID, TargetKind: kind, RequestedKey: requestedKeyParam(cmd.RequestedKey), Payload: cmd.Payload, PayloadHash: cmd.PayloadHash, ExpectedRevision: cmd.ExpectedRevision, Actor: cmd.Actor})
	if err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	devices, err := q.ListStoreBoundDevices(ctx, sid)
	if err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	if cmd.TargetKind == catalogadmin.TargetKindCreate && len(devices) > 1 {
		// Phase 17-R3 §13: creation is single-flight per intent. Update
		// commands are idempotent across devices (same entity, same
		// revision), but two devices applying the same create would mint
		// two divergent UUIDs under one code in two separate SQLite
		// databases — unmergeable without operator surgery. Constrain the
		// create to ONE authoritative target (lowest device UUID,
		// deterministic): duplicate delivery of one intent can never
		// create multiple authoritative entities in a Store. Trade-off,
		// documented: if that device never polls, the command stays
		// PENDING (cancellable while pending) until the operator retries.
		// The code-uniqueness fence stays the last line of defense for
		// genuinely distinct commands racing on one code.
		devices = devices[:1]
	}
	for _, dev := range devices {
		tid, _ := parseUUID((ids.System{}).New())
		if _, err = q.CreateCatalogAdminTarget(ctx, sqlcgen.CreateCatalogAdminTargetParams{ID: tid, CommandID: id, DeviceID: dev.DeviceID}); err != nil {
			return catalogadmin.CommandView{}, catalogAdminErr(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return catalogadmin.CommandView{}, catalogAdminErr(err)
	}
	kind, requested, result := commandIdentity(row.TargetKind, row.RequestedKey, row.ResultEntityID)
	return catalogadmin.CommandView{
		ID: uuidString(row.ID), StoreID: uuidString(row.StoreID),
		Type: row.CommandType, Version: int(row.CommandVersion),
		EntityID: row.EntityID, TargetKind: kind, RequestedKey: requested, ResultEntityID: result,
		PayloadHash:      row.PayloadHash,
		ExpectedRevision: row.ExpectedRevision, Actor: row.Actor,
		Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// requestedKeyParam maps the creation requested key to a nullable TEXT
// param (NULL for entity commands, preserving the coherence invariant).
func requestedKeyParam(key string) pgtype.Text {
	if key == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: key, Valid: true}
}

// ---- Phase 17 variant admin reads ----

// SetCommandResultEntity records a command's actual resulting entity ID
// when one of its targets reports APPLIED (Phase 17-R3 create closure).
// First writer wins; the intent row is never rewritten.
func (d Devices) SetCommandResultEntity(ctx context.Context, targetID, entityID string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tid, err := parseUUID(targetID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(entityID) == "" {
		return nil
	}
	if _, err := parseUUID(entityID); err != nil {
		return err
	}
	q := sqlcgen.New(d.pool)
	_, err = q.SetCommandResultEntity(ctx, sqlcgen.SetCommandResultEntityParams{
		TargetID: tid, ResultEntityID: strings.TrimSpace(entityID),
	})
	return catalogAdminErr(err)
}

// CatalogAdminDeviceStates is the Phase 19 batched device read: one
// set-based lookup for many target devices replaces the per-target
// DeviceActive/BindingStore/Capability/DeviceName round-trips.
func (d Devices) CatalogAdminDeviceStates(ctx context.Context, deviceIDs []string) (map[string]catalogadmin.DeviceState, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	ids := make([]pgtype.UUID, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		uid, err := parseUUID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uid)
	}
	rows, err := sqlcgen.New(d.pool).CatalogAdminDeviceStates(ctx, ids)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make(map[string]catalogadmin.DeviceState, len(rows))
	for _, row := range rows {
		out[uuidString(row.ID)] = catalogadmin.DeviceState{
			DeviceID: uuidString(row.ID), Name: row.Name, Active: row.Status == "active",
			StoreID: storeOrEmpty(row.StoreID), Capable: row.CatalogAdminCommandsV1,
		}
	}
	return out, nil
}
