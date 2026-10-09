package app

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// NewHumanAuthService wires human authentication (ADR-0053) from
// validated configuration. It is shared by the HTTP server and the
// server-side `auth` CLI (bootstrap-owner, reset-password).
func NewHumanAuthService(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*humanauth.Service, error) {
	box, err := humanauth.NewSecretBox(cfg.AuthMFAEncryptionKey)
	if err != nil {
		return nil, err
	}
	csrfKey, err := dashboard.DeriveKey(cfg.Pepper, "moonlight-cloud/csrf-v1")
	if err != nil {
		return nil, err
	}
	policy := humanauth.DefaultPolicy()
	policy.IdleTimeout, policy.AbsoluteTimeout = cfg.AuthSessionIdleTimeout, cfg.AuthSessionAbsoluteTimeout
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return humanauth.NewService(postgres.NewHumanAuth(pool, 10*time.Second), box, csrfKey, clock.System{}, ids.System{}, policy, log), nil
}
