// Package settings holds business-wide dashboard settings: which sales
// features the business uses (tax, discounts, returns) and the USD->EGP
// rates entered in Cloud. Both apply to every Store.
package settings

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Business says which sales features the business uses. Reports show a
// feature's rows when it is on, or when the period still holds its amounts.
type Business struct {
	TaxEnabled      bool      `json:"tax_enabled"`
	DiscountEnabled bool      `json:"discount_enabled"`
	ReturnsEnabled  bool      `json:"returns_enabled"`
	Revision        int64     `json:"revision"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ExchangeRate is one Cloud-entered USD->EGP rate.
type ExchangeRate struct {
	ID            string    `json:"id"`
	Rate          string    `json:"rate"`
	RateMicrorate int64     `json:"rate_microrate"`
	EffectiveAt   time.Time `json:"effective_at"`
	Note          string    `json:"note"`
	CreatedAt     time.Time `json:"created_at"`
}

// NewRate is a validated rate ready to store.
type NewRate struct {
	ID            string
	RateMicrorate int64
	EffectiveAt   time.Time
	Note          string
	CreatedBy     string
}

// Repository stores the settings. UpdateBusiness returns ok=false when
// expectedRevision is stale; DeleteRate returns ok=false when absent.
type Repository interface {
	GetBusiness(ctx context.Context) (Business, error)
	UpdateBusiness(ctx context.Context, next Business, expectedRevision int64, actorID string) (Business, bool, error)
	ListRates(ctx context.Context, limit int32) ([]ExchangeRate, error)
	AddRate(ctx context.Context, rate NewRate) error
	DeleteRate(ctx context.Context, id string) (bool, error)
}

// MaxRates bounds the rate history the dashboard reads at once.
const MaxRates = 200

// MaxMicrorate keeps a typed rate sane (1,000,000 EGP per USD).
const MaxMicrorate = 1_000_000 * 1_000_000

// Service validates and applies settings changes.
type Service struct {
	repo  Repository
	newID func() string
	now   func() time.Time
}

// NewService wires the settings service.
func NewService(repo Repository, newID func() string, now func() time.Time) Service {
	return Service{repo: repo, newID: newID, now: now}
}

// Business returns the current business settings.
func (s Service) Business(ctx context.Context) (Business, error) { return s.repo.GetBusiness(ctx) }

// UpdateBusiness saves the toggles when expectedRevision is current.
func (s Service) UpdateBusiness(ctx context.Context, next Business, expectedRevision int64, actorID string) (Business, error) {
	if expectedRevision < 1 {
		return Business{}, apperr.New(apperr.InvalidInput, "revision is required")
	}
	saved, ok, err := s.repo.UpdateBusiness(ctx, next, expectedRevision, actorID)
	if err != nil {
		return Business{}, err
	}
	if !ok {
		return Business{}, apperr.New(apperr.Conflict, "SETTINGS_CHANGED")
	}
	return saved, nil
}

// Rates lists the newest rates first.
func (s Service) Rates(ctx context.Context) ([]ExchangeRate, error) {
	rates, err := s.repo.ListRates(ctx, MaxRates)
	if err != nil {
		return nil, err
	}
	for i := range rates {
		rates[i].Rate = FormatMicrorate(rates[i].RateMicrorate)
	}
	return rates, nil
}

// AddRate stores a rate typed as a decimal ("50.25"). A zero effectiveAt
// means now; a duplicate effective time is a conflict.
func (s Service) AddRate(ctx context.Context, rate string, effectiveAt time.Time, note, actorID string) (ExchangeRate, error) {
	micro, err := ParseMicrorate(rate)
	if err != nil {
		return ExchangeRate{}, err
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 120 {
		return ExchangeRate{}, apperr.New(apperr.InvalidInput, "note must be at most 120 characters")
	}
	if effectiveAt.IsZero() {
		effectiveAt = s.now()
	}
	if effectiveAt.After(s.now().Add(366 * 24 * time.Hour)) {
		return ExchangeRate{}, apperr.New(apperr.InvalidInput, "effective_at is too far in the future")
	}
	created := NewRate{ID: s.newID(), RateMicrorate: micro, EffectiveAt: effectiveAt.UTC(), Note: note, CreatedBy: actorID}
	if err := s.repo.AddRate(ctx, created); err != nil {
		return ExchangeRate{}, err
	}
	return ExchangeRate{ID: created.ID, Rate: FormatMicrorate(micro), RateMicrorate: micro,
		EffectiveAt: created.EffectiveAt, Note: note, CreatedAt: s.now().UTC()}, nil
}

// DeleteRate removes a rate entered by mistake.
func (s Service) DeleteRate(ctx context.Context, id string) error {
	ok, err := s.repo.DeleteRate(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.New(apperr.NotFound, "exchange rate not found")
	}
	return nil
}

// ParseMicrorate reads a positive decimal with at most six decimals into
// millionths, exactly (no floating point).
func ParseMicrorate(raw string) (int64, error) {
	bad := apperr.New(apperr.InvalidInput, "rate must be a positive number with at most 6 decimals")
	whole, frac, _ := strings.Cut(strings.TrimSpace(raw), ".")
	if whole == "" || len(frac) > 6 || strings.ContainsAny(whole+frac, "+- ") {
		return 0, bad
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > MaxMicrorate/1_000_000 {
		return 0, bad
	}
	var f int64
	if frac != "" {
		if f, err = strconv.ParseInt(frac+strings.Repeat("0", 6-len(frac)), 10, 64); err != nil {
			return 0, bad
		}
	}
	micro := w*1_000_000 + f
	if micro <= 0 || micro > MaxMicrorate {
		return 0, bad
	}
	return micro, nil
}

// FormatMicrorate renders millionths as a decimal without trailing zeros.
func FormatMicrorate(micro int64) string {
	whole := strconv.FormatInt(micro/1_000_000, 10)
	frac := strings.TrimRight(strconv.FormatInt(1_000_000+micro%1_000_000, 10)[1:], "0")
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}
