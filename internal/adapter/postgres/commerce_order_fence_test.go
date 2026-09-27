package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// barrierOrderProvider holds worker A's GET response until released,
// modeling a delayed provider read while worker B completes.
type barrierOrderProvider struct {
	key       commerce.ProviderKey
	stateA    orders.OrderSnapshot
	stateB    orders.OrderSnapshot
	releaseA  chan struct{}
	aReturned chan struct{}
	mu        sync.Mutex
	calls     int
}

func (s *barrierOrderProvider) Key() commerce.ProviderKey { return s.key }

func (s *barrierOrderProvider) UpsertProduct(_ context.Context, _ commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	return commerce.ProductUpsertResult{}, nil
}

func (s *barrierOrderProvider) SetInventory(_ context.Context, _ commerce.InventoryUpdateRequest) error {
	return nil
}

func (s *barrierOrderProvider) GetOrder(_ context.Context, _ string) (orders.OrderSnapshot, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		<-s.releaseA
		close(s.aReturned)
		return s.stateA, nil
	}
	return s.stateB, nil
}

func fenceService(env *saleEnv, provider *barrierOrderProvider) *orders.OrderService {
	registry := commerce.NewRegistry()
	if err := registry.Register(provider.key, provider); err != nil {
		panic(err)
	}
	return orders.NewOrderService(registry, catalogStore(env), nilLogger())
}

// TestReconcileDelayedGetBarrier is the F-01 gate: A begins first and
// its GET stalls; B begins later, fetches B, and commits; A returns
// late with a different fingerprint and must be superseded.
func TestReconcileDelayedGetBarrier(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	stateA := orderTestSnapshot("website", "910", orders.StatusPending, "pending", 10000)
	stateB := orderTestSnapshot("website", "910", orders.StatusProcessing, "processing", 12000)
	stateB.Lines = append(stateB.Lines, orders.OrderLine{
		ExternalLineID: 2, ExternalProductID: "501", SKU: "PAP-2", Name: "Second", Quantity: 1, TotalMinor: 2000,
	})
	provider := &barrierOrderProvider{
		key: "website", stateA: stateA, stateB: stateB,
		releaseA: make(chan struct{}), aReturned: make(chan struct{}),
	}
	service := fenceService(env, provider)

	type result struct {
		outcome orders.ReconcileResult
		err     error
	}
	aDone := make(chan result, 1)
	go func() {
		gen, err := store.BeginOrderReconcile(ctx, "website", "910")
		if err != nil {
			aDone <- result{err: err}
			return
		}
		snapshot, err := provider.GetOrder(ctx, "910")
		if err != nil {
			aDone <- result{err: err}
			return
		}
		outcome, err := store.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), gen)
		aDone <- result{outcome: orders.ReconcileResult{Revision: outcome.Revision, Changed: outcome.Changed, Superseded: outcome.Superseded}, err: err}
	}()
	// Let A's GET block inside the provider call.
	time.Sleep(200 * time.Millisecond)
	// B starts later, fetches current B, and commits fully.
	bResult, err := service.ReconcileOrder(ctx, "website", "910")
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	if bResult.Superseded || bResult.Revision != 1 || !bResult.Changed {
		t.Fatalf("B commits: %+v", bResult)
	}
	close(provider.releaseA)
	<-provider.aReturned
	aResult := <-aDone
	if aResult.err != nil {
		t.Fatalf("A must supersede cleanly, not fail: %v", aResult.err)
	}
	if !aResult.outcome.Superseded || aResult.outcome.Changed {
		t.Fatalf("A superseded: %+v", aResult.outcome)
	}
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "910")
	if err != nil {
		t.Fatal(err)
	}
	if stored.TotalMinor != 12000 || len(stored.Lines) != 2 || rev != 1 {
		t.Fatalf("final is B: %+v rev %d", stored, rev)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='910'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("no spurious A history: %d (%v)", history, err)
	}
}

// TestReconcileCommitOrderInversion proves the fence does not depend on
// HTTP finish order: A begins gen1, B begins gen2, A returns first and
// is superseded before B even completes.
func TestReconcileCommitOrderInversion(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	stateA := orderTestSnapshot("website", "911", orders.StatusPending, "pending", 10000)
	stateB := orderTestSnapshot("website", "911", orders.StatusProcessing, "processing", 12000)
	genA, err := store.BeginOrderReconcile(ctx, "website", "911")
	if err != nil {
		t.Fatal(err)
	}
	genB, err := store.BeginOrderReconcile(ctx, "website", "911")
	if err != nil {
		t.Fatal(err)
	}
	if genB != genA+1 {
		t.Fatalf("generations increment: %d %d", genA, genB)
	}
	// A returns first while B is still in flight: superseded sight
	// unseen, before B completes.
	outcomeA, err := store.ReconcileProjectedOrder(ctx, stateA, orders.Fingerprint(stateA), genA)
	if err != nil {
		t.Fatal(err)
	}
	if !outcomeA.Superseded {
		t.Fatalf("A superseded: %+v", outcomeA)
	}
	// B eventually commits B.
	outcomeB, err := store.ReconcileProjectedOrder(ctx, stateB, orders.Fingerprint(stateB), genB)
	if err != nil {
		t.Fatal(err)
	}
	if outcomeB.Superseded || outcomeB.Revision != 1 || !outcomeB.Changed {
		t.Fatalf("B commits: %+v", outcomeB)
	}
	stored, _, _, err := store.LoadProjectedOrder(ctx, "website", "911")
	if err != nil {
		t.Fatal(err)
	}
	if stored.TotalMinor != 12000 {
		t.Fatalf("final is B: %+v", stored)
	}
}

// TestReconcileSameStateConcurrent proves two generations fetching
// identical state converge on one revision with one history row: the
// newest generation projects, the older is superseded.
func TestReconcileSameStateConcurrent(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	snapshot := orderTestSnapshot("website", "912", orders.StatusProcessing, "processing", 12000)
	genA, err := store.BeginOrderReconcile(ctx, "website", "912")
	if err != nil {
		t.Fatal(err)
	}
	genB, err := store.BeginOrderReconcile(ctx, "website", "912")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := orders.Fingerprint(snapshot)
	outcomeA, err := store.ReconcileProjectedOrder(ctx, snapshot, fingerprint, genA)
	if err != nil {
		t.Fatal(err)
	}
	outcomeB, err := store.ReconcileProjectedOrder(ctx, snapshot, fingerprint, genB)
	if err != nil {
		t.Fatal(err)
	}
	if outcomeA.Superseded == outcomeB.Superseded {
		t.Fatalf("exactly one wins: A=%+v B=%+v", outcomeA, outcomeB)
	}
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "912")
	if err != nil || rev != 1 || stored.TotalMinor != 12000 {
		t.Fatalf("one semantic state: %+v %d %v", stored, rev, err)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='912'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("one history row: %d (%v)", history, err)
	}
}

// TestReconcileThreeGenerations proves only the current generation can
// mutate when responses complete out of order (C, then A, then B).
func TestReconcileThreeGenerations(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	stateC := orderTestSnapshot("website", "913", orders.StatusCompleted, "completed", 15000)
	stateA := orderTestSnapshot("website", "913", orders.StatusPending, "pending", 10000)
	stateB := orderTestSnapshot("website", "913", orders.StatusProcessing, "processing", 12000)
	genA, err := store.BeginOrderReconcile(ctx, "website", "913")
	if err != nil {
		t.Fatal(err)
	}
	genB, err := store.BeginOrderReconcile(ctx, "website", "913")
	if err != nil {
		t.Fatal(err)
	}
	genC, err := store.BeginOrderReconcile(ctx, "website", "913")
	if err != nil {
		t.Fatal(err)
	}
	// Completion order C, A, B: only C may mutate.
	outcomeC, err := store.ReconcileProjectedOrder(ctx, stateC, orders.Fingerprint(stateC), genC)
	if err != nil || outcomeC.Superseded || outcomeC.Revision != 1 {
		t.Fatalf("C commits: %+v %v", outcomeC, err)
	}
	outcomeA, err := store.ReconcileProjectedOrder(ctx, stateA, orders.Fingerprint(stateA), genA)
	if err != nil || !outcomeA.Superseded {
		t.Fatalf("A superseded: %+v %v", outcomeA, err)
	}
	outcomeB, err := store.ReconcileProjectedOrder(ctx, stateB, orders.Fingerprint(stateB), genB)
	if err != nil || !outcomeB.Superseded {
		t.Fatalf("B superseded: %+v %v", outcomeB, err)
	}
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "913")
	if err != nil || rev != 1 || stored.TotalMinor != 15000 || stored.Canonical != orders.StatusCompleted {
		t.Fatalf("final is C: %+v %d %v", stored, rev, err)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='913'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("no regression history: %d (%v)", history, err)
	}
}

// TestManualSyncVsWebhook proves later-started current-state
// reconciliations win regardless of trigger: webhook A, then manual
// sync-order B, then webhook A returns.
func TestManualSyncVsWebhook(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	stateA := orderTestSnapshot("website", "914", orders.StatusPending, "pending", 10000)
	stateB := orderTestSnapshot("website", "914", orders.StatusProcessing, "processing", 12000)
	provider := &barrierOrderProvider{
		key: "website", stateA: stateA, stateB: stateB,
		releaseA: make(chan struct{}), aReturned: make(chan struct{}),
	}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	manual := orders.NewOrderService(registry, store, nilLogger())
	type result struct {
		outcome orders.ReconcileOutcome
		err     error
	}
	aDone := make(chan result, 1)
	go func() {
		gen, err := store.BeginOrderReconcile(ctx, "website", "914")
		if err != nil {
			aDone <- result{err: err}
			return
		}
		snapshot, err := provider.GetOrder(ctx, "914")
		if err != nil {
			aDone <- result{err: err}
			return
		}
		outcome, err := store.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), gen)
		aDone <- result{outcome: orders.ReconcileOutcome{Revision: outcome.Revision, Changed: outcome.Changed, Superseded: outcome.Superseded}, err: err}
	}()
	time.Sleep(200 * time.Millisecond)
	// Manual sync-order starts later and completes first.
	bResult, err := manual.ReconcileOrder(ctx, "website", "914")
	if err != nil {
		t.Fatalf("manual: %v", err)
	}
	if bResult.Superseded {
		t.Fatalf("manual wins: %+v", bResult)
	}
	close(provider.releaseA)
	<-provider.aReturned
	aResult := <-aDone
	if aResult.err != nil || !aResult.outcome.Superseded {
		t.Fatalf("webhook superseded: %+v %v", aResult.outcome, aResult.err)
	}
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "914")
	if err != nil || rev != 1 || stored.TotalMinor != 12000 {
		t.Fatalf("final is manual B: %+v %d %v", stored, rev, err)
	}
}

// TestFailedNewerSupersedesOlder proves a newer generation whose
// provider GET fails still blocks the older attempt: staleness wins
// over availability, and the previous committed order remains.
func TestFailedNewerSupersedesOlder(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	committed := orderTestSnapshot("website", "915", orders.StatusProcessing, "processing", 12000)
	gen0, err := store.BeginOrderReconcile(ctx, "website", "915")
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := store.ReconcileProjectedOrder(ctx, committed, orders.Fingerprint(committed), gen0); err != nil || outcome.Revision != 1 {
		t.Fatalf("baseline: %+v %v", outcome, err)
	}
	genA, err := store.BeginOrderReconcile(ctx, "website", "915")
	if err != nil {
		t.Fatal(err)
	}
	genB, err := store.BeginOrderReconcile(ctx, "website", "915")
	if err != nil {
		t.Fatal(err)
	}
	// B's GET fails (Temporary): B never projects, but A's older token
	// is already stale.
	stale := orderTestSnapshot("website", "915", orders.StatusPending, "pending", 10000)
	outcomeA, err := store.ReconcileProjectedOrder(ctx, stale, orders.Fingerprint(stale), genA)
	if err != nil || !outcomeA.Superseded {
		t.Fatalf("A superseded: %+v %v", outcomeA, err)
	}
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "915")
	if err != nil || rev != 1 || stored.TotalMinor != 12000 {
		t.Fatalf("previous committed order remains: %+v %d %v", stored, rev, err)
	}
	// B retries with a fresh generation and converges.
	_ = genB
	genC, err := store.BeginOrderReconcile(ctx, "website", "915")
	if err != nil {
		t.Fatal(err)
	}
	fresh := orderTestSnapshot("website", "915", orders.StatusCompleted, "completed", 12000)
	outcomeC, err := store.ReconcileProjectedOrder(ctx, fresh, orders.Fingerprint(fresh), genC)
	if err != nil || outcomeC.Superseded || outcomeC.Revision != 2 {
		t.Fatalf("retry converges: %+v %v", outcomeC, err)
	}
}

// TestBeginOrderReconcileConcurrent proves concurrent beginners receive
// unique strictly increasing generations for one order, while different
// orders and providers stay independent and unblocked.
func TestBeginOrderReconcileConcurrent(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	const racers = 16
	results := make(chan orders.ReconcileGeneration, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			generation, err := store.BeginOrderReconcile(ctx, "website", "920")
			if err != nil {
				t.Errorf("begin: %v", err)
				return
			}
			results <- generation
		}()
	}
	wg.Wait()
	close(results)
	seen := map[orders.ReconcileGeneration]bool{}
	var max orders.ReconcileGeneration
	for generation := range results {
		if seen[generation] {
			t.Fatalf("duplicate generation %d", generation)
		}
		seen[generation] = true
		if generation > max {
			max = generation
		}
	}
	if len(seen) != racers || max != orders.ReconcileGeneration(racers) {
		t.Fatalf("unique 1..%d, got max %d count %d", racers, max, len(seen))
	}
	// Independent orders and providers do not share generation space.
	genOther, err := store.BeginOrderReconcile(ctx, "website", "921")
	if err != nil || genOther != 1 {
		t.Fatalf("independent order: %d %v", genOther, err)
	}
	genCross, err := store.BeginOrderReconcile(ctx, "shop2", "920")
	if err != nil || genCross != 1 {
		t.Fatalf("cross-provider isolation: %d %v", genCross, err)
	}
}
