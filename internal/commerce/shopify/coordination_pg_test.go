package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type r1CoordinatedMappings struct {
	*stubMappings
	commerce.ProductSyncCoordinator
}
type r1MutableSource struct {
	mu      sync.Mutex
	desired commerce.DesiredProduct
}

func (s *r1MutableSource) GetDesiredCommerceProduct(_ context.Context, _ string) (commerce.DesiredProduct, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.desired, nil
}
func (s *r1MutableSource) set(d commerce.DesiredProduct) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.desired = d
}

func TestR1CrossInstanceSequence(t *testing.T) {
	for _, window := range []string{"title-description", "price", "fence", "unpublish", "publish", "safe-zero", "inventory-only"} {
		t.Run(window, func(t *testing.T) {
			database := testutil.Isolated(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			a, err := pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			b, err := pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			coordA, coordB := postgres.NewDevices(a, 5*time.Second), postgres.NewDevices(b, 5*time.Second)
			h := newHarness(t)
			pa, err := NewShopifyProvider(testConfig(), harnessClient(t, h), coordA)
			if err != nil {
				t.Fatal(err)
			}
			pb, err := NewShopifyProvider(testConfig(), harnessClient(t, h), coordB)
			if err != nil {
				t.Fatal(err)
			}
			maps := newStubMappings()
			source := &r1MutableSource{desired: fixedDesired("019c0000-0000-7000-8000-000000000011", "PAP-001")}
			source.desired.InventoryRevision = 3
			source.desired.Availability.OnlineAvailable = 5
			sa := commerce.NewCommerceService(newRegistryWith(t, pa), r1CoordinatedMappings{maps, coordA}, source, nil)
			sb := commerce.NewCommerceService(newRegistryWith(t, pb), r1CoordinatedMappings{maps, coordB}, source, nil)
			initial, err := sa.SyncProduct(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011")
			if err != nil {
				t.Fatal(err)
			}
			old := source.desired
			old.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "OLD-TITLE"}}
			old.Product.Descriptions = map[string]string{"en": "old-description"}
			if window == "unpublish" {
				old.Published = false
			}
			source.set(old)
			entered, release := make(chan struct{}), make(chan struct{})
			var fired atomic.Bool
			h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var request gqlRequest
				_ = json.Unmarshal(raw, &request)
				operation := operationName(request.Query)
				target := "MoonlightProductUpdate"
				switch window {
				case "price":
					target = "MoonlightManagedVariantUpdate"
				case "fence":
					target = "MoonlightMetafieldsSet"
				case "unpublish":
					target = "MoonlightUnpublish"
				case "publish":
					target = "MoonlightPublish"
				case "safe-zero", "inventory-only":
					target = "MoonlightInventorySet"
				}
				if operation == target && fired.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
				}
				h.serve(w, r)
			})
			doneOld := make(chan error, 1)
			go func() {
				_, err := sa.SyncProduct(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011")
				doneOld <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("old request never reached barrier")
			}
			fresh := fixedDesired("019c0000-0000-7000-8000-000000000011", "PAP-001")
			fresh.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "NEW-TITLE"}}
			fresh.Product.Descriptions = map[string]string{"en": "new-description"}
			fresh.Product.Prices = []commerce.Money{{Currency: "EGP", AmountMinor: 70000}}
			fresh.CatalogRevision, fresh.PolicyRevision, fresh.InventoryRevision = 9, 9, 9
			fresh.Availability.OnlineAvailable = 20
			if window == "inventory-only" {
				fresh.CatalogRevision, fresh.PolicyRevision = 3, 2
			}
			if window == "publish" {
				fresh.Published = false
				fresh.Availability.OnlineAvailable = 0
			}
			source.set(fresh)
			doneNew := make(chan error, 1)
			go func() {
				_, err := sb.SyncProduct(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011")
				doneNew <- err
			}()
			// Observe actual database lock contention, not a timing-based assumption.
			for {
				var waiting int
				err := b.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted").Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("second instance did not wait for DB coordination")
				default:
				}
			}
			select {
			case err := <-doneNew:
				t.Fatalf("newer sequence bypassed whole-operation lock: %v", err)
			default:
			}
			close(release)
			if err := <-doneOld; err != nil {
				t.Fatal(err)
			}
			if err := <-doneNew; err != nil {
				t.Fatal(err)
			}
			state := h.remoteState(initial.ExternalProductID)
			wantQuantity := int64(20)
			if !fresh.Published {
				wantQuantity = 0
			}
			if state.title != "NEW-TITLE" || state.desc != "new-description" || state.price != "700.00" || state.quantity != wantQuantity {
				t.Fatalf("newer state regressed: %+v", state)
			}
			h.mu.Lock()
			published := h.published[initial.ExternalProductID]
			inv := h.products[initial.ExternalProductID].metafields[metafieldNamespace+".inventory_revision"]
			h.mu.Unlock()
			if published != fresh.Published || inv != "9" {
				t.Fatal("publication/inventory fence regressed")
			}
			// A stale prepared adapter request arriving after newer completion must fail.
			stale := inventoryRequest(initial.ExternalProductID, "019c0000-0000-7000-8000-000000000011", 5, true)
			stale.InventoryRevision = 3
			stale.CatalogRevision, stale.PolicyRevision = fresh.CatalogRevision, fresh.PolicyRevision
			if err := pa.SetInventory(ctx, stale); err == nil {
				t.Fatal("old inventory-only revision accepted")
			}
			if h.remoteState(initial.ExternalProductID).quantity != wantQuantity {
				t.Fatal("stale inventory mutated newer quantity")
			}
		})
	}
}

func TestR1CoordinationCancellation(t *testing.T) {
	database := testutil.Isolated(t)
	ctx, timeout := context.WithTimeout(context.Background(), 10*time.Second)
	defer timeout()
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	coord := postgres.NewDevices(pool, time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- coord.WithProductSync(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011", func(context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("coordinator did not enter callback: %v", err)
	case <-ctx.Done():
		t.Fatal("coordinator callback timed out")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := coord.WithProductSync(cancelled, "shopify-main", "019c0000-0000-7000-8000-000000000011", func(context.Context) error { t.Fatal("cancelled waiter ran"); return nil }); err == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := coord.WithProductSync(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011", func(context.Context) error { return nil }); err != nil {
		t.Fatal("lock not released", err)
	}
}

func TestR1LostCoordinationSession(t *testing.T) {
	database := testutil.Isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	coord := postgres.NewDevices(a, 5*time.Second)
	h := newHarness(t)
	p, err := NewShopifyProvider(testConfig(), harnessClient(t, h), coord)
	if err != nil {
		t.Fatal(err)
	}
	err = coord.WithProductSync(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011", func(held context.Context) error {
		var pid int
		if err := b.QueryRow(ctx, `SELECT pid FROM pg_locks WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		var killed bool
		if err := b.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&killed); err != nil || !killed {
			t.Fatal("session termination fixture", err)
		}
		_, err := p.UpsertProduct(held, upsertRequest("019c0000-0000-7000-8000-000000000011", "PAP-001", true))
		return err
	})
	if err == nil {
		t.Fatal("lost coordination session allowed provider work")
	}
	if got := len(h.recorded()); got != 0 {
		t.Fatal("provider network occurred after lost session", got)
	}
	if err := postgres.NewDevices(b, 5*time.Second).WithProductSync(ctx, "shopify-main", "019c0000-0000-7000-8000-000000000011", func(context.Context) error { return nil }); err != nil {
		t.Fatal("lost lock did not release", err)
	}
}
