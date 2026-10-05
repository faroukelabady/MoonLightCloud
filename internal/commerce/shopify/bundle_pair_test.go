package shopify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	httpadapter "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/shopify"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	storemodel "github.com/faroukelabady/MoonLightCloud/internal/store"
	syncmodel "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Optional external fixture is freshly exported by the candidate Retail
// canonical service, never a handwritten replacement for actual outbox bytes.
func TestR3ActualRetailCloudProviderPair(t *testing.T) {
	input := os.Getenv("PHASE15_OUTBOX_EXPORT")
	if input == "" {
		t.Skip("PHASE15_OUTBOX_EXPORT unset: run with candidate Retail durable export")
	}
	ctx := context.Background()
	database := testutil.Isolated(t)
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	d := postgres.NewDevices(pool, 5*time.Second)
	hasher, err := auth.NewHasher(bytes.Repeat([]byte{'r'}, 32))
	if err != nil {
		t.Fatal(err)
	}
	authentication := auth.NewService(d, hasher, "", 1, clock.System{}, ids.System{})
	device, err := authentication.Create(ctx, "candidate-retail")
	if err != nil {
		t.Fatal(err)
	}
	storeID := "019c0000-0000-7000-8000-000000000091"
	if _, err = d.RegisterStore(ctx, device.Device.ID, storemodel.RegistrationRequest{StoreID: storeID, DisplayName: "Candidate", Timezone: "Africa/Cairo"}); err != nil {
		t.Fatal(err)
	}
	validators := map[string]func(json.RawMessage) error{
		catalog.EventCategorySnapshotV2: func(raw json.RawMessage) error {
			p, e := catalog.DecodeCategorySnapshotV2(raw)
			if e != nil {
				return e
			}
			_, e = catalog.ValidateCategorySnapshot(p)
			return e
		},
		catalog.EventProductSnapshotV1: func(raw json.RawMessage) error {
			p, e := catalog.DecodeProductSnapshot(raw)
			if e != nil {
				return e
			}
			_, e = catalog.ValidateProductSnapshot(p)
			return e
		},
		catalog.EventProductSalesPolicySnapshotV1: func(raw json.RawMessage) error {
			p, e := catalog.DecodeProductSalesPolicySnapshot(raw)
			if e != nil {
				return e
			}
			_, e = catalog.ValidateProductSalesPolicySnapshot(p)
			return e
		},
		catalog.EventInventoryProductSnapshotV1: func(raw json.RawMessage) error {
			p, e := catalog.DecodeProductInventorySnapshot(raw)
			if e != nil {
				return e
			}
			_, e = catalog.ValidateProductInventorySnapshot(p)
			return e
		},
		catalog.EventProductConfigurationSnapshotV1: func(raw json.RawMessage) error {
			p, e := catalog.DecodeProductConfigurationsSnapshot(raw)
			if e != nil {
				return e
			}
			_, e = catalog.ValidateProductConfigurationsSnapshot(p)
			return e
		},
	}
	for typ, validator := range validators {
		syncmodel.RegisterEventType(typ, validator)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/sync/batches", httpadapter.DeviceAuth(authentication)(httpadapter.SyncBatch(syncmodel.NewService(d, clock.System{}), nil)))
	server := httptest.NewServer(mux)
	defer server.Close()
	token := device.Device.ID + "." + device.Credential.ID + "." + device.RawSecret
	type event struct {
		ID      string          `json:"event_id"`
		Type    string          `json:"event_type"`
		Payload json.RawMessage `json:"payload"`
	}
	readAndProject := func(path string) string {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var export struct {
			Events  []event `json:"events"`
			Product string  `json:"product_id"`
		}
		if e = json.Unmarshal(raw, &export); e != nil {
			t.Fatal(e)
		}
		var envelope map[string]json.RawMessage
		if e = json.Unmarshal(raw, &envelope); e != nil {
			t.Fatal(e)
		}
		body, e := json.Marshal(map[string]json.RawMessage{"events": envelope["events"]})
		if e != nil {
			t.Fatal(e)
		}
		for replay := 0; replay < 2; replay++ {
			request, e := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/sync/batches", bytes.NewReader(body))
			if e != nil {
				t.Fatal(e)
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response, e := server.Client().Do(request)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("authenticated durable ingestion: %d", response.StatusCode)
			}
		}
		priority := map[string]int{catalog.EventCategorySnapshotV2: 0, catalog.EventProductSnapshotV1: 1, catalog.EventProductSalesPolicySnapshotV1: 2, catalog.EventInventoryProductSnapshotV1: 3, catalog.EventProductConfigurationSnapshotV1: 4}
		sort.SliceStable(export.Events, func(i, j int) bool { return priority[export.Events[i].Type] < priority[export.Events[j].Type] })
		for _, item := range export.Events {
			record, found, e := d.LoadCatalogEvent(ctx, item.ID)
			if e != nil || !found {
				t.Fatal(e)
			}
			var result catalog.ProjectResult
			switch item.Type {
			case catalog.EventCategorySnapshotV2:
				result, e = d.ProjectCategory(ctx, record, time.Now())
			case catalog.EventProductSnapshotV1:
				result, e = d.ProjectProduct(ctx, record, time.Now())
			case catalog.EventProductSalesPolicySnapshotV1:
				result, e = d.ProjectProductSalesPolicy(ctx, record, time.Now())
			case catalog.EventInventoryProductSnapshotV1:
				result, e = d.ProjectProductInventory(ctx, record, time.Now())
			case catalog.EventProductConfigurationSnapshotV1:
				result, e = d.ProjectProductConfigurations(ctx, record, time.Now())
			default:
				t.Fatal("unexpected exported event")
			}
			if e != nil || (result.Outcome != catalog.OutcomeProcessed && result.Outcome != catalog.OutcomeAlready) {
				t.Fatalf("project actual %s: %+v %v", item.Type, result, e)
			}
		}
		return export.Product
	}
	productID := readAndProject(input)
	f := shopify.NewR3BundleFixture(t)
	p := f.Provider(t, d)
	registry := commerce.NewRegistry()
	if err = registry.Register(p.Key(), p); err != nil {
		t.Fatal(err)
	}
	service := commerce.NewCommerceService(registry, d, commerce.NewCatalogCommerceSource(catalog.NewService(d)), nil)
	// Run the production durable reevaluation worker. The observing wrapper
	// forwards every database operation and signals only after fenced commit.
	runQueue := func() {
		queue := &r3ObservedQueue{ProductReevaluationStore: d, done: make(chan error, 1)}
		worker := commerce.NewReevaluationWorker(queue, service, registry, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
		runCtx, cancel := context.WithCancel(ctx)
		joined := make(chan struct{})
		go func() { defer close(joined); worker.Run(runCtx) }()
		select {
		case e := <-queue.done:
			if e != nil {
				cancel()
				<-joined
				t.Fatal(e)
			}
		case <-time.After(10 * time.Second):
			cancel()
			<-joined
			t.Fatal("durable reevaluation did not converge")
		}
		cancel()
		<-joined
	}
	runQueue()
	result, e := d.GetProductMapping(ctx, p.Key(), productID)
	if e != nil || result.ExternalProductID == "" || f.Creates() != 1 {
		t.Fatal("actual pair did not converge", e)
	}
	choices, e := d.CatalogProductConfigurations(ctx, productID)
	if e != nil || len(choices) != 1 {
		t.Fatal("actual Retail configuration not projected")
	}
	mapping, e := d.GetProductConfigurationMapping(ctx, p.Key(), productID, choices[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	freshOrder := func(status orders.CanonicalStatus) orders.OrderSnapshot {
		return orders.OrderSnapshot{ProviderKey: string(p.Key()), ExternalOrderID: "150031", ProviderStatus: string(status), Canonical: status, Currency: "EGP", TotalMinor: 130000, CreatedAt: now, ModifiedAt: now, Lines: []orders.OrderLine{{ExternalLineID: 1, ExternalProductID: result.ExternalProductID, ProviderConfigurationID: mapping.ExternalConfigurationID, Name: "Papyrus", Quantity: 1, TotalMinor: 130000}}}
	}
	reconcile := func(snapshot orders.OrderSnapshot) {
		generation, e := d.BeginOrderReconcile(ctx, snapshot.ProviderKey, snapshot.ExternalOrderID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = d.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), generation); e != nil {
			t.Fatal(e)
		}
	}
	reconcile(freshOrder(orders.StatusPending))
	before, e := d.GetOrderDetail(ctx, string(p.Key()), "150031")
	if e != nil || before.Lines[0].FrameStyleNameEN == nil || *before.Lines[0].FrameStyleNameEN != "Original" {
		t.Fatal("initial historical selection not captured")
	}
	if updated := readAndProject(input + ".updated"); updated != productID {
		t.Fatal("Retail identity changed")
	}
	// Restart the provider/service with a genuinely reopened database pool
	// after the updated durable events have committed.
	pool.Close()
	pool, err = pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	d = postgres.NewDevices(pool, 5*time.Second)
	p = f.Provider(t, d)
	registry = commerce.NewRegistry()
	if err = registry.Register(p.Key(), p); err != nil {
		t.Fatal(err)
	}
	service = commerce.NewCommerceService(registry, d, commerce.NewCatalogCommerceSource(catalog.NewService(d)), nil)
	runQueue()
	reconcile(freshOrder(orders.StatusProcessing))
	after, e := d.GetOrderDetail(ctx, string(p.Key()), "150031")
	if e != nil {
		t.Fatal(e)
	}
	if after.Lines[0].FrameStyleNameEN == nil || *after.Lines[0].FrameStyleNameEN != "Original" || after.Lines[0].ConfigurationPriceDeltaMinor == nil || *after.Lines[0].ConfigurationPriceDeltaMinor != "30000" {
		t.Fatal("fresh status refresh substituted current rename/reprice/disable")
	}
	scoped, e := d.GetOrderDetailForStore(ctx, storeID, string(p.Key()), "150031")
	if e != nil || scoped.Summary.MappingComplete != after.Summary.MappingComplete || scoped.Lines[0].ConfigurationPriceDeltaMinor == nil || *scoped.Lines[0].ConfigurationPriceDeltaMinor != "30000" {
		t.Fatal("Store/global historical verdict differs", e)
	}
	globalCounts, e := d.CountOrdersByStatus(ctx, string(p.Key()))
	if e != nil {
		t.Fatal(e)
	}
	scopedCounts, e := d.CountOrdersByStatusForStore(ctx, storeID, string(p.Key()))
	if e != nil || len(globalCounts) != len(scopedCounts) {
		t.Fatal("Store/global count mismatch", e)
	}
	other, e := d.ListOrderSummariesForStore(ctx, "019c0000-0000-7000-8000-000000000092", string(p.Key()), "", 10, nil)
	if e != nil || len(other) != 0 {
		t.Fatal("unknown Store widened scope", e)
	}
	if f.ChoiceCount(result.ExternalProductID) != 1 {
		t.Fatal("disabled selection still offered")
	}
	t.Log("candidate Retail SQLite outbox -> authenticated HTTP -> PostgreSQL projection/reevaluation -> public CommerceService/TLS convergence -> order capture -> actual Retail update -> immutable status refresh")
}

// No fake queue state: all claims and finishes remain real PostgreSQL writes.
type r3ObservedQueue struct {
	commerce.ProductReevaluationStore
	done chan error
}

func (q *r3ObservedQueue) CompleteProductReevaluation(ctx context.Context, claim commerce.ProductReevaluation) (bool, error) {
	ok, err := q.ProductReevaluationStore.CompleteProductReevaluation(ctx, claim)
	if err == nil && !ok {
		err = commerce.ConflictError("reevaluation completion rejected")
	}
	q.done <- err
	return ok, err
}
func (q *r3ObservedQueue) RetryProductReevaluation(ctx context.Context, claim commerce.ProductReevaluation, next time.Time, code string) (bool, error) {
	ok, err := q.ProductReevaluationStore.RetryProductReevaluation(ctx, claim, next, code)
	q.done <- commerce.TemporaryError("reevaluation failed: " + code)
	return ok, err
}
