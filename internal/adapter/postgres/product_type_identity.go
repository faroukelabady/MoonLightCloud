package postgres

import (
	"context"
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The seeded Retail identity is shared by installations. Only its Cloud
// storage key is scoped; APIs, commands and historical snapshots keep the
// exact Retail identity. Operator-created types never use this exception.
const sharedProductTypeID = "10000000-0000-4000-8000-000000000001"

// ErrProductTypeIdentityCollision means the internal seed key is already
// occupied without the authoritative provenance needed to use that key.
// Callers must block the event; retrying must never overwrite its occupant.
var ErrProductTypeIdentityCollision = errors.New("product type identity collision")

func canonicalDefaultProductTypeID(id string, store pgtype.UUID) string {
	if id != sharedProductTypeID || !store.Valid {
		return id
	}
	return uuid.NewSHA1(defaultNamespaceUUID, []byte(uuidString(store)+":product-type:"+id)).String()
}

// Preserve an already owned raw projection, including its current Product
// references. New scoped projections use a deterministic internal key.
// An unowned legacy default is never assigned to a Store by this resolver.
func projectedProductTypeID(ctx context.Context, q *sqlcgen.Queries, id string, store pgtype.UUID) (string, error) {
	if id != sharedProductTypeID {
		uid, err := parseUUID(id)
		if err != nil {
			return "", err
		}
		row, err := q.CatalogProductTypeIdentity(ctx, uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
		// A proven internal key is never writable raw Retail intent. Check
		// the row's own provenance, including for unscoped legacy events.
		// Existing custom identities with no seed source remain addressable.
		if reservedProductTypeStorageID(id, row.StoreID) && sharedProductTypeSource(row.SourceTypeID, row.SourceStoreID, row.StoreID) {
			return "", ErrProductTypeIdentityCollision
		}
		return id, nil
	}
	if !store.Valid {
		row, err := q.CatalogProductTypeIdentity(ctx, mustProductTypeUUID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
		// Legacy traffic may still own its unscoped raw row. It cannot
		// overwrite a Store's already-owned raw seed through the frozen
		// general projection wildcard rule.
		if row.StoreID.Valid {
			return "", ErrProductTypeIdentityCollision
		}
		return id, nil
	}
	row, err := q.CatalogProductTypeIdentity(ctx, mustProductTypeUUID(id))
	if err == nil && row.StoreID.Valid && row.StoreID.Bytes == store.Bytes {
		if !sharedProductTypeSource(row.SourceTypeID, row.SourceStoreID, store) {
			return "", ErrProductTypeIdentityCollision
		}
		return id, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	canonical := canonicalDefaultProductTypeID(id, store)
	row, err = q.CatalogProductTypeIdentity(ctx, mustProductTypeUUID(canonical))
	if errors.Is(err, pgx.ErrNoRows) {
		return canonical, nil
	}
	if err != nil {
		return "", err
	}
	if !row.StoreID.Valid || row.StoreID.Bytes != store.Bytes || !sharedProductTypeSource(row.SourceTypeID, row.SourceStoreID, store) {
		return "", ErrProductTypeIdentityCollision
	}
	return canonical, nil
}

func reservedProductTypeStorageID(id string, store pgtype.UUID) bool {
	return store.Valid && id != sharedProductTypeID && id == canonicalDefaultProductTypeID(sharedProductTypeID, store)
}

func sharedProductTypeSource(sourceTypeID string, sourceStore, store pgtype.UUID) bool {
	return sourceTypeID == sharedProductTypeID && sourceStore.Valid && store.Valid && sourceStore.Bytes == store.Bytes
}

func mustProductTypeUUID(id string) pgtype.UUID {
	// Called only for the fixed seed UUID or its deterministic valid key.
	value, _ := parseUUID(id)
	return value
}

func retailProductTypeID(id string, store pgtype.UUID, sourceTypeID string, sourceStore pgtype.UUID) string {
	if reservedProductTypeStorageID(id, store) && sharedProductTypeSource(sourceTypeID, sourceStore, store) {
		return sharedProductTypeID
	}
	return id
}
