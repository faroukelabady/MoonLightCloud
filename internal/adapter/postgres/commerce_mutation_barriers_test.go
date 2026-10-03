package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func TestCommerceMutationBarrierDurabilityAndFencing(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := NewDevices(env.pool, 5*time.Second)
	product := commerceProductID(0x117f)
	var oldCtx context.Context
	var oldToken string
	err := store.WithProductSync(ctx, "shopify-main", product, func(held context.Context) error {
		oldCtx = held
		var err error
		oldToken, err = commerce.BeginProductMutation(held, strings.Repeat("a", 64))
		if err != nil {
			return err
		}
		var n int
		if err := env.pool.QueryRow(ctx, "SELECT count(*) FROM commerce_product_mutation_barriers WHERE operation_id=$1 AND state='in_flight'", oldToken).Scan(&n); err != nil || n != 1 {
			t.Fatal("barrier not committed before send", n, err)
		}
		return commerce.TemporaryError("fixture interruption")
	})
	if err == nil {
		t.Fatal("fixture interruption ignored")
	}
	got, err := store.GetProductMutationBarrier(ctx, "shopify-main", product)
	if err != nil || got.OperationID != oldToken || got.State != "uncertain" {
		t.Fatal("uncertainty not durable", got, err)
	}
	for _, key := range []string{"shopify-main"} {
		if err := store.WithProductSync(ctx, commerce.ProviderKey(key), strings.ToUpper(product), func(context.Context) error { t.Fatal("case variant bypassed durable identity"); return nil }); err == nil {
			t.Fatal("blocked work accepted")
		}
	}
	if err := store.WithProductSync(ctx, "woo-main", product, func(context.Context) error { return nil }); err != nil {
		t.Fatal("provider isolation broken", err)
	}
	for _, bad := range []struct {
		op, res   string
		confirmed bool
	}{{oldToken, "remote_completed", false}, {oldToken, "force", true}, {commerceProductID(0x1180), "remote_completed", true}} {
		if err := store.ResolveProductMutationBarrier(ctx, "shopify-main", product, bad.op, bad.res, bad.confirmed); err == nil {
			t.Fatal("invalid operator resolution accepted")
		}
	}
	if err := store.ResolveProductMutationBarrier(ctx, "shopify-main", strings.ToUpper(product), oldToken, "remote_not_applied", true); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveProductMutationBarrier(ctx, "shopify-main", product, oldToken, "remote_not_applied", true); err != nil {
		t.Fatal("resolution replay not idempotent", err)
	}
	if err := store.ResolveProductMutationBarrier(ctx, "shopify-main", product, oldToken, "remote_completed", true); err == nil {
		t.Fatal("contradictory resolution accepted")
	}
	var newToken string
	err = store.WithProductSync(ctx, "shopify-main", product, func(held context.Context) error {
		var err error
		newToken, err = commerce.BeginProductMutation(held, strings.Repeat("b", 64))
		if err != nil {
			return err
		}
		if err := commerce.CompleteProductMutation(oldCtx, oldToken); err == nil {
			t.Fatal("late old completion accepted")
		}
		if err := commerce.CompleteProductMutation(held, newToken); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal("new operation affected by stale owner", err)
	}
	status, err := store.GetProductMutationBarrier(ctx, "shopify-main", product)
	if err != nil || status.OperationID != "" {
		t.Fatal("successful response left barrier", status, err)
	}
	var history int
	if err := env.pool.QueryRow(ctx, "SELECT count(*) FROM commerce_product_mutation_barriers WHERE operation_id=$1 AND state='resolved' AND resolution='remote_not_applied'", oldToken).Scan(&history); err != nil || history != 1 {
		t.Fatal("history erased", history, err)
	}
}

func TestCommerceMutationBarrierMissingCompletionBlocks(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := NewDevices(env.pool, 5*time.Second)
	product := commerceProductID(0x1181)
	err := store.WithProductSync(ctx, "shopify-main", product, func(held context.Context) error {
		_, err := commerce.BeginProductMutation(held, strings.Repeat("c", 64))
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "COMMERCE_MUTATION_UNCERTAIN") {
		t.Fatal("forgotten completion reported success", err)
	}
	status, err := store.GetProductMutationBarrier(ctx, "shopify-main", product)
	if err != nil || status.OperationID == "" {
		t.Fatal("missing completion lost evidence", status, err)
	}
}

func TestCommerceMutationResolverWaitsForLiveSequence(t *testing.T) {
	env := openSaleEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDevices(env.pool, 5*time.Second)
	product := commerceProductID(0x1182)
	entered, release := make(chan string, 1), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.WithProductSync(ctx, "shopify-main", product, func(held context.Context) error {
			token, err := commerce.BeginProductMutation(held, strings.Repeat("d", 64))
			if err != nil {
				return err
			}
			entered <- token
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return commerce.TemporaryError("fixture before send interruption")
		})
	}()
	token := <-entered
	resolved := make(chan error, 1)
	go func() {
		resolved <- store.ResolveProductMutationBarrier(ctx, "shopify-main", product, token, "remote_not_applied", true)
	}()
	waitFor(t, 2*time.Second, func() bool {
		var n int
		err := env.pool.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())").Scan(&n)
		return err == nil && n > 0
	}, "resolver waits on live sequence")
	select {
	case err := <-resolved:
		t.Fatal("operator resolved through live sequence", err)
	default:
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("fixture interruption ignored")
	}
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	status, err := store.GetProductMutationBarrier(ctx, "shopify-main", product)
	if err != nil || status.OperationID != "" {
		t.Fatal("settled resolution incomplete", status, err)
	}
	// Old resolution replay must not clear a different active request.
	var newer string
	_ = store.WithProductSync(ctx, "shopify-main", product, func(held context.Context) error {
		var err error
		newer, err = commerce.BeginProductMutation(held, strings.Repeat("e", 64))
		if err != nil {
			return err
		}
		return commerce.TemporaryError("new uncertainty")
	})
	if err := store.ResolveProductMutationBarrier(ctx, "shopify-main", product, token, "remote_not_applied", true); err != nil {
		t.Fatal(err)
	}
	status, err = store.GetProductMutationBarrier(ctx, "shopify-main", product)
	if err != nil || status.OperationID != newer {
		t.Fatal("old resolution cleared newer request", status, err)
	}
}
