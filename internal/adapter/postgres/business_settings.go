package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/settings"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BusinessSettings stores the business-wide dashboard settings and rates.
type BusinessSettings struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var _ settings.Repository = BusinessSettings{}

// NewBusinessSettings shares the application's pool.
func NewBusinessSettings(pool *pgxpool.Pool, timeout time.Duration) BusinessSettings {
	return BusinessSettings{pool: pool, timeout: timeout}
}

func (d BusinessSettings) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}

func optionalUUID(id string) pgtype.UUID {
	u, err := parseUUID(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

// GetBusiness reads the single settings row.
func (d BusinessSettings) GetBusiness(ctx context.Context) (settings.Business, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).GetBusinessSettings(ctx)
	if err != nil {
		return settings.Business{}, reportErr("business settings", err)
	}
	return settings.Business{TaxEnabled: r.TaxEnabled, DiscountEnabled: r.DiscountEnabled,
		ReturnsEnabled: r.ReturnsEnabled, Revision: r.Revision, UpdatedAt: r.UpdatedAt.Time}, nil
}

// UpdateBusiness saves the toggles when expectedRevision is current.
func (d BusinessSettings) UpdateBusiness(ctx context.Context, next settings.Business, expectedRevision int64, actorID string) (settings.Business, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).UpdateBusinessSettings(ctx, sqlcgen.UpdateBusinessSettingsParams{
		TaxEnabled: next.TaxEnabled, DiscountEnabled: next.DiscountEnabled, ReturnsEnabled: next.ReturnsEnabled,
		UpdatedBy: optionalUUID(actorID), Revision: expectedRevision,
	})
	if err != nil {
		return settings.Business{}, false, reportErr("update business settings", err)
	}
	if len(rows) == 0 {
		return settings.Business{}, false, nil
	}
	r := rows[0]
	return settings.Business{TaxEnabled: r.TaxEnabled, DiscountEnabled: r.DiscountEnabled,
		ReturnsEnabled: r.ReturnsEnabled, Revision: r.Revision, UpdatedAt: r.UpdatedAt.Time}, true, nil
}

// ListRates returns the newest rates first.
func (d BusinessSettings) ListRates(ctx context.Context, limit int32) ([]settings.ExchangeRate, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListExchangeRates(ctx, limit)
	if err != nil {
		return nil, reportErr("exchange rates", err)
	}
	out := make([]settings.ExchangeRate, 0, len(rows))
	for _, r := range rows {
		out = append(out, settings.ExchangeRate{ID: uuidString(r.ID), RateMicrorate: r.RateMicrorate,
			EffectiveAt: r.EffectiveAt.Time, Note: r.Note.String, CreatedAt: r.CreatedAt.Time})
	}
	return out, nil
}

// AddRate stores one rate; a second rate at the same instant conflicts.
func (d BusinessSettings) AddRate(ctx context.Context, rate settings.NewRate) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	id, err := parseUUID(rate.ID)
	if err != nil {
		return apperr.New(apperr.Internal, "exchange rate id")
	}
	err = sqlcgen.New(d.pool).InsertExchangeRate(ctx, sqlcgen.InsertExchangeRateParams{
		ID: id, RateMicrorate: rate.RateMicrorate, EffectiveAt: pgTime(rate.EffectiveAt),
		Note: pgText(rate.Note), CreatedBy: optionalUUID(rate.CreatedBy),
	})
	if isUniqueViolation(err) {
		return apperr.New(apperr.Conflict, "RATE_TIME_TAKEN")
	}
	if err != nil {
		return reportErr("add exchange rate", err)
	}
	return nil
}

// DeleteRate removes one rate.
func (d BusinessSettings) DeleteRate(ctx context.Context, id string) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return false, apperr.New(apperr.InvalidInput, "id must be a UUID")
	}
	n, err := sqlcgen.New(d.pool).DeleteExchangeRate(ctx, uid)
	if err != nil {
		return false, reportErr("delete exchange rate", err)
	}
	return n > 0, nil
}
