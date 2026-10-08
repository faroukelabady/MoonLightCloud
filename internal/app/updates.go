package app

import (
	"crypto/ed25519"
	"log/slog"
	"strings"
	"time"

	adapterhttp "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/fleetupdate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/release"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newUpdateHandlers wires the Phase 18 fleet service. Release import is
// fail-closed without configured trusted public keys; device update
// channels still work (they only relay already-imported releases).
func newUpdateHandlers(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *adapterhttp.UpdateHandlers {
	trust, trustErr := parseReleaseTrust(cfg.ReleaseTrustedPublicKeys)
	if trustErr != nil {
		log.Warn("release trust keys invalid; release import disabled")
	} else {
		log.Info("release trust", "keys", trust.KeyIDs())
	}
	svc := fleetupdate.NewService(postgres.NewFleetUpdates(pool, 10*time.Second), trust, trustErr,
		time.Now, ids.System{}.New, fleetupdate.Config{AllowHTTPArtifacts: cfg.Environment == config.EnvDevelopment})
	return &adapterhttp.UpdateHandlers{Svc: svc, Now: time.Now}
}

func parseReleaseTrust(raw string) (release.TrustSet, error) {
	var keys []ed25519.PublicKey
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		pub, err := release.ParsePublicKey(part)
		if err != nil {
			empty, _ := release.NewTrustSet()
			return empty, err
		}
		keys = append(keys, pub)
	}
	return release.NewTrustSet(keys...)
}
