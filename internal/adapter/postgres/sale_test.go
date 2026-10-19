package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	istore "github.com/faroukelabady/MoonLightCloud/internal/store"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testEventRegistry() isync.Registry {
	return isync.NewRegistry(map[string]isync.Validator{
		isync.DiagnosticEventType: nil,
		sale.EventSaleFinalizedV3: func(raw json.RawMessage) error {
			p, err := sale.DecodeV3(raw)
			if err != nil {
				return err
			}
			_, err = sale.ValidateV3(p)
			return err
		},
		returnrefund.EventReturnRefundFinalizedV1: func(raw json.RawMessage) error {
			p, err := returnrefund.Decode(raw)
			if err != nil {
				return err
			}
			_, err = returnrefund.Validate(p)
			return err
		},
		catalog.EventCategorySnapshotV2: func(raw json.RawMessage) error {
			decoded, err := catalog.DecodeCategorySnapshotV2(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateCategorySnapshot(decoded)
			return err
		},
		catalog.EventTagSnapshotV1: func(raw json.RawMessage) error {
			p, err := catalog.DecodeTagSnapshot(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateTagSnapshot(p)
			return err
		},
		catalog.EventProductSnapshotV2: func(raw json.RawMessage) error {
			p, err := catalog.DecodeProductSnapshotV2(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateProductSnapshotV2(p)
			return err
		},
		catalog.EventProductSalesPolicySnapshotV1: func(raw json.RawMessage) error {
			p, err := catalog.DecodeProductSalesPolicySnapshot(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateProductSalesPolicySnapshot(p)
			return err
		},
		catalog.EventProductConfigurationSnapshotV1: func(raw json.RawMessage) error {
			decoded, err := catalog.DecodeProductConfigurationsSnapshot(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateProductConfigurationsSnapshot(decoded)
			return err
		},
		catalog.EventProductVariantSnapshotV1: func(raw json.RawMessage) error {
			p, err := catalog.DecodeProductVariantSnapshot(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateProductVariantSnapshot(p)
			return err
		},
		catalog.EventInventoryProductVariantSnapshotV1: func(raw json.RawMessage) error {
			p, err := catalog.DecodeProductVariantInventorySnapshot(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateProductVariantInventorySnapshot(p)
			return err
		},
		catalog.EventProductTypeSnapshotV1: func(raw json.RawMessage) error {
			p, err := catalog.DecodeProductTypeSnapshot(raw)
			if err != nil {
				return err
			}
			_, err = catalog.ValidateProductTypeSnapshot(p)
			return err
		},
	})
}

// saleEnv provisions a device and returns sync service, projector wiring,
// device/credential IDs, and a drain helper.
type saleEnv struct {
	pool    *pgxpool.Pool
	syncSvc isync.Service
	proj    *sale.Projector
	devID   string
	credID  string
	storeID string
}

func openSaleEnv(t *testing.T) *saleEnv {
	t.Helper()
	pool, authSvc := openTestRepo(t)
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	store := NewDevices(pool, 5*time.Second)
	storeID := bindTestStore(t, store, p.Device.ID)
	return &saleEnv{
		pool:    pool,
		syncSvc: isync.NewService(NewSyncInbox(pool, 5*time.Second), clock.System{}, testEventRegistry()),
		proj: sale.NewProjector(NewSaleProjection(store.pool, store.timeout), clock.System{},
			nilLogger()),
		devID: p.Device.ID, credID: p.Credential.ID, storeID: storeID,
	}
}

// bindTestStore registers a fresh Store for a test device: every upload
// needs a Store binding (ADR-0055).
func bindTestStore(t *testing.T, devices Devices, deviceID string) string {
	t.Helper()
	storeID := uuid.NewString()
	if _, err := NewStoreRegistry(devices.pool, devices.timeout).RegisterStore(context.Background(), deviceID, istore.RegistrationRequest{
		StoreID: storeID, DisplayName: "Test Store", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatalf("bind test store: %v", err)
	}
	return storeID
}

func (e *saleEnv) ingest(t *testing.T, eventID, payload string) isync.BatchResult {
	t.Helper()
	payload = withSaleTags(t, payload)
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v3","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, payload)
	res, err := e.syncSvc.Ingest(context.Background(), e.devID, e.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return res
}

func (e *saleEnv) drain(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.proj.Run(ctx) }()
	// Stop once the projector's own discovery query finds no due work (an
	// in-flight event stays visible until its transaction commits). The old
	// fixed 2s settle remains the upper bound, so events that never settle
	// (not-due pending, failing loads) behave exactly as before.
	pending := NewSaleProjection(e.pool, 5*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ids, err := pending.PendingSaleV3Events(context.Background(), 1); err == nil && len(ids) == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-done
}

func saleCount(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nilLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testSystemClock() clock.Clock { return clock.System{} }

// waitFor polls cond until true or timeout (no arbitrary test sleeps).
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../sale/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
