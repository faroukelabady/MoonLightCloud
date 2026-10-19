package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ProjectProductConfigurations projects one Phase 15 configuration
// snapshot (the complete ONLINE product-option set of one Product at one
// aggregate configuration revision). One atomic claim+project
// SERIALIZABLE transaction: aggregate revision gate under the frozen
// rules (stale no-ops; equal revisions compare normalized semantic
// state; contradictions block), replace-set of configuration rows, and —
// when effective option state actually changed — a durable commerce
// re-evaluation intent in the SAME transaction (crash-safe; no provider
// I/O inside the transaction).
func (d CatalogProjection) ProjectProductConfigurations(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductConfigurationsSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductConfigurationsSnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	puid, perr := parseUUID(valid.ProductID)
	if perr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, "product_id must be a UUID")
	}
	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}
	lockKeys := [][2]string{{"product", valid.ProductID}}
	for _, entry := range valid.Configurations {
		lockKeys = append(lockKeys, [2]string{"product-configuration", entry.ConfigurationID})
	}
	if err := lockCatalogEntities(ctx, q, lockKeys...); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}
	// The Product must already exist: configurations never invent
	// Products (§7) and never carry their own identity.
	productRow, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "product not yet projected")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed", err)
	}
	// Phase 15-R1 F17: the TRUSTED ingress Store context recorded with
	// the accepted event must authorize mutation of this Product's
	// configurations. The payload never supplies Store identity and the
	// device's CURRENT binding is never re-derived here.
	if _, scopeOK := resolveProjectionScope(productRow.StoreID, event.StoreID); !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "configuration event not authorized for product store")
	}
	// Phase 15-R1 F17: configuration ID → Product ownership is immutable.
	// A supplied ID already owned by another Product/Store is rejected —
	// an upsert may never reassign product_id.
	for _, entry := range valid.Configurations {
		configurationUID, err := parseUUID(entry.ConfigurationID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, "configuration_id must be a UUID")
		}
		row, err := q.CatalogProductConfigurationByID(ctx, configurationUID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				_ = tx.Rollback(ctx)
				return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration ownership lookup failed", err)
			}
			continue
		}
		if uuidString(row.ProductID) != valid.ProductID {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "configuration identity owned by another product")
		}
	}
	storedRows, err := q.CatalogProductConfigurationsByProduct(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration state read failed", err)
	}
	storedRevision := productRow.ConfigurationRevision
	if valid.ConfigurationRevision < storedRevision {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if valid.ConfigurationRevision == storedRevision {
		if !reflect.DeepEqual(catalog.NormalizeProductConfigurations(valid), normalizeStoredConfigurations(puid, storedRows, storedRevision)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	fingerprint := catalog.FingerprintProductConfigurations(valid)
	if err := q.DeleteCatalogProductConfigurations(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration replace failed", err)
	}
	for _, entry := range valid.Configurations {
		cuid, cerr := parseUUID(entry.ConfigurationID)
		if cerr != nil {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, "configuration_id must be a UUID")
		}
		params := sqlcgen.UpsertCatalogProductConfigurationParams{
			ConfigurationID: cuid, ProductID: puid, Kind: entry.Kind,
			StyleCode: entry.StyleCode, StyleNameAr: entry.StyleNameAR,
			ColorCode: entry.ColorCode, ColorNameAr: entry.ColorNameAR,
			PriceDeltaEgpMinor: entry.PriceDeltaEGPCents,
			Enabled:            entry.Enabled, Position: int32(entry.Position),
			ConfigurationRevision: entry.ConfigurationRevision,
			SourceRevision:        valid.ConfigurationRevision, SourceEventID: attempt.euid,
			SourceDeviceID: attempt.duid, SourcePayloadHash: fingerprint[:],
			SourceReceivedAt: pgTime(event.ReceivedAt),
		}
		if entry.StyleNameEN != nil {
			params.StyleNameEn = pgText(*entry.StyleNameEN)
		}
		if entry.ColorNameEN != nil {
			params.ColorNameEn = pgText(*entry.ColorNameEN)
		}
		if entry.PriceDeltaUSDCents != nil {
			params.PriceDeltaUsdMinor = pgtype.Int8{Int64: *entry.PriceDeltaUSDCents, Valid: true}
		}
		written, err := q.UpsertCatalogProductConfiguration(ctx, params)
		if err != nil {
			_ = tx.Rollback(ctx)
			if isUniqueViolation(err) {
				return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "configuration identity collision")
			}
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration upsert failed", err)
		}
		// The guarded ON CONFLICT clause never moves product_id (F17):
		// zero affected rows means the ID belongs to another Product.
		if written == 0 {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "configuration identity owned by another product")
		}
	}
	if _, err := q.BumpCatalogProductConfigurationRevision(ctx, sqlcgen.BumpCatalogProductConfigurationRevisionParams{
		ProductID: puid, ConfigurationRevision: valid.ConfigurationRevision,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration revision write failed", err)
	}
	// Durable commerce re-evaluation (§147): coalesced per Product in the
	// projection transaction. The worker reads CURRENT state at run time
	// (§149), so rapid mutation sequences converge to the latest durable
	// projection (§150).
	if err := q.EnqueueProductReevaluation(ctx, sqlcgen.EnqueueProductReevaluationParams{
		ProductID: puid, StoreID: productRow.StoreID, Reason: "product_configuration",
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "reevaluation enqueue failed: "+err.Error(), err)
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// normalizeStoredConfigurations reconstructs comparison form from the
// projected configuration rows (frozen equal-revision semantics).
func normalizeStoredConfigurations(productID pgtype.UUID, rows []sqlcgen.CatalogProductConfigurationsByProductRow, storedRevision int64) catalog.NormalizedProductConfigurations {
	entries := make([]catalog.ProductConfigurationEntry, 0, len(rows))
	for _, row := range rows {
		entry := catalog.ProductConfigurationEntry{
			ConfigurationID: uuidString(row.ConfigurationID), Kind: row.Kind,
			StyleCode: row.StyleCode, StyleNameAR: row.StyleNameAr,
			ColorCode: row.ColorCode, ColorNameAR: row.ColorNameAr,
			PriceDeltaEGPCents: row.PriceDeltaEgpMinor,
			Enabled:            row.Enabled, Position: int(row.Position),
			ConfigurationRevision: row.ConfigurationRevision,
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
		entries = append(entries, entry)
	}
	return catalog.NormalizeProductConfigurations(catalog.ProductConfigurationsSnapshot{
		ProductID: uuidString(productID), Configurations: entries,
		ConfigurationRevision: storedRevision,
	})
}
