package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// A transaction-scoped advisory lock needs no new durable schema. The dedicated
// connection owns it until the complete callback ends or the connection closes.
// Hash collisions only over-serialize unrelated products; they cannot weaken it.
func (d Devices) WithProductSync(ctx context.Context, provider commerce.ProviderKey, product string, work func(context.Context) error) error {
	if commerce.ProductSyncHeld(ctx, provider, product) {
		return work(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return redact(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	digest := sha256.Sum256([]byte("commerce-product\x00" + string(provider) + "\x00" + product))
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(digest[:8]))); err != nil {
		return redact(err)
	}
	held := commerce.CoordinatedProductContext(ctx, provider, product)
	held = commerce.WithProductSyncGuard(held, func(check context.Context) error {
		if _, err := tx.Exec(check, "SELECT 1"); err != nil {
			cancel()
			return commerce.TemporaryError("commerce coordination session lost")
		}
		return nil
	})
	if err := work(held); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return redact(err)
	}
	return nil
}
