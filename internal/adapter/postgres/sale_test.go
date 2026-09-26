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
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) {
	// Mirror app wiring: sale validation runs before ACK in this binary.
	isync.RegisterEventType(sale.EventSaleFinalizedV1, func(raw json.RawMessage) error {
		p, err := sale.Decode(raw)
		if err != nil {
			return err
		}
		_, err = sale.Validate(p)
		return err
	})
	isync.RegisterEventType(returnrefund.EventReturnRefundFinalizedV1, func(raw json.RawMessage) error {
		p, err := returnrefund.Decode(raw)
		if err != nil {
			return err
		}
		_, err = returnrefund.Validate(p)
		return err
	})
	isync.RegisterEventType(catalog.EventCategorySnapshotV1, func(raw json.RawMessage) error {
		p, err := catalog.DecodeCategorySnapshot(raw)
		if err != nil {
			return err
		}
		_, err = catalog.ValidateCategorySnapshot(p)
		return err
	})
	isync.RegisterEventType(catalog.EventTagSnapshotV1, func(raw json.RawMessage) error {
		p, err := catalog.DecodeTagSnapshot(raw)
		if err != nil {
			return err
		}
		_, err = catalog.ValidateTagSnapshot(p)
		return err
	})
	isync.RegisterEventType(catalog.EventProductSnapshotV1, func(raw json.RawMessage) error {
		p, err := catalog.DecodeProductSnapshot(raw)
		if err != nil {
			return err
		}
		_, err = catalog.ValidateProductSnapshot(p)
		return err
	})
	os.Exit(m.Run())
}

// saleEnv provisions a device and returns sync service, projector wiring,
// device/credential IDs, and a drain helper.
type saleEnv struct {
	pool    *pgxpool.Pool
	syncSvc isync.Service
	proj    *sale.Projector
	devID   string
	credID  string
}

func openSaleEnv(t *testing.T) *saleEnv {
	t.Helper()
	pool, authSvc := openTestRepo(t)
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	store := NewDevices(pool, 5*time.Second)
	return &saleEnv{
		pool:    pool,
		syncSvc: isync.NewService(store, clock.System{}),
		proj: sale.NewProjector(store, clock.System{},
			nilLogger()),
		devID: p.Device.ID, credID: p.Credential.ID,
	}
}

func (e *saleEnv) ingest(t *testing.T, eventID, payload string) isync.BatchResult {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
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
	// Let the projector scan and settle, then stop it.
	time.Sleep(2 * time.Second)
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
