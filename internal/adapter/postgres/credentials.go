package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Credential methods live on Devices (the transactional Store).

// CreateCredential persists one credential row.
func (d Devices) CreateCredential(ctx context.Context, cred auth.Credential) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return insertCredential(ctx, sqlcgen.New(d.pool), cred)
}

func insertCredential(ctx context.Context, q *sqlcgen.Queries, cred auth.Credential) error {
	id, err := parseUUID(cred.ID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "credential id must be a UUID")
	}
	deviceID, err := parseUUID(cred.DeviceID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	if len(cred.Salt) == 0 || len(cred.Verifier) == 0 {
		return apperr.New(apperr.InvalidInput, "credential salt and verifier are required")
	}
	err = q.CreateCredential(ctx, sqlcgen.CreateCredentialParams{
		ID: id, DeviceID: deviceID, Salt: cred.Salt, Verifier: cred.Verifier,
		VerifierVersion: int32(cred.VerifierVersion), PepperVersion: int32(cred.PepperVersion),
		Status: cred.Status, CreatedAt: pgTime(cred.CreatedAt), ActivatedAt: pgTime(cred.ActivatedAt),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.New(apperr.Conflict, "credential already exists")
		}
		return apperr.Wrap(apperr.Internal, "create credential", redact(err))
	}
	return nil
}

// CredentialByID loads a credential or NotFound (auth failures stay generic
// at the service layer).
func (d Devices) CredentialByID(ctx context.Context, id string) (auth.Credential, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return auth.Credential{}, apperr.New(apperr.NotFound, "device not found")
	}
	row, err := sqlcgen.New(d.pool).CredentialByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Credential{}, apperr.New(apperr.NotFound, "device not found")
		}
		return auth.Credential{}, apperr.Wrap(apperr.Internal, "load credential", redact(err))
	}
	return toCredential(row), nil
}

// ActiveCredentials lists active credentials of a device.
func (d Devices) ActiveCredentials(ctx context.Context, deviceID string) ([]auth.Credential, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(deviceID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	rows, err := sqlcgen.New(d.pool).ActiveCredentialsForDevice(ctx, uid)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list credentials", redact(err))
	}
	out := make([]auth.Credential, 0, len(rows))
	for _, r := range rows {
		out = append(out, toCredential(sqlcgen.DeviceCredential{
			ID: r.ID, DeviceID: r.DeviceID, Salt: r.Salt, Verifier: r.Verifier,
			VerifierVersion: r.VerifierVersion, PepperVersion: r.PepperVersion,
			Status: r.Status, CreatedAt: r.CreatedAt, ActivatedAt: r.ActivatedAt,
			RevokedAt: r.RevokedAt, LastUsedAt: r.LastUsedAt,
		}))
	}
	return out, nil
}

// CredentialsForDevice lists all credentials of a device for operators.
func (d Devices) CredentialsForDevice(ctx context.Context, deviceID string) ([]auth.Credential, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(deviceID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	rows, err := sqlcgen.New(d.pool).CredentialsForDevice(ctx, uid)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list credentials", redact(err))
	}
	out := make([]auth.Credential, 0, len(rows))
	for _, r := range rows {
		out = append(out, toCredential(sqlcgen.DeviceCredential{
			ID: r.ID, DeviceID: r.DeviceID, Salt: r.Salt, Verifier: r.Verifier,
			VerifierVersion: r.VerifierVersion, PepperVersion: r.PepperVersion,
			Status: r.Status, CreatedAt: r.CreatedAt, ActivatedAt: r.ActivatedAt,
			RevokedAt: r.RevokedAt, LastUsedAt: r.LastUsedAt,
		}))
	}
	return out, nil
}

// TouchCredentialLastUsed refreshes last_used_at.
func (d Devices) TouchCredentialLastUsed(ctx context.Context, id string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "credential id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).TouchCredentialLastUsed(ctx, sqlcgen.TouchCredentialLastUsedParams{
		ID: uid, LastUsedAt: pgTime(at),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "touch credential", redact(err))
	}
	return nil
}

// RevokeCredential revokes one credential row.
func (d Devices) RevokeCredential(ctx context.Context, id string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "credential id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).RevokeCredential(ctx, sqlcgen.RevokeCredentialParams{
		ID: uid, RevokedAt: pgTime(at),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "revoke credential", redact(err))
	}
	return nil
}

func toCredential(row sqlcgen.DeviceCredential) auth.Credential {
	c := auth.Credential{
		ID: uuidString(row.ID), DeviceID: uuidString(row.DeviceID),
		Salt: row.Salt, Verifier: row.Verifier,
		VerifierVersion: int(row.VerifierVersion), PepperVersion: int(row.PepperVersion),
		Status: row.Status, CreatedAt: row.CreatedAt.Time, ActivatedAt: row.ActivatedAt.Time,
	}
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		c.RevokedAt = &t
	}
	if row.LastUsedAt.Valid {
		t := row.LastUsedAt.Time
		c.LastUsedAt = &t
	}
	return c
}
