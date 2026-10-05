package shopify

// Phase 15-R4 regressions for the two open R3 Medium findings:
//
//	F07 — a definitive failed create receipt must not permanently strand
//	      a distinct corrected intent (liveness), while the identical
//	      failed intent is never blindly replayed and unknown side
//	      effects keep their fences.
//	F20 — the sellable bundle parent converges its canonical content
//	      (title/description) and required activation; unpublication
//	      never drafts it and unrelated merchant fields survive.
//
// Both run through the PUBLIC UpsertProduct boundary with real
// PostgreSQL and production GraphQL documents against the stateful
// fixture (documented semantics only — never a live store).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newReceiptStack provisions the real-PostgreSQL receipt stack; the
// returned factory binds whatever pool is current, so reopened pools
// exercise genuine fresh connections.
func newReceiptStack(t *testing.T) (*bundleFixture, string, func(**pgxpool.Pool) *ShopifyProvider) {
	t.Helper()
	f := newBundleFixture(t)
	provider := func(pool **pgxpool.Pool) *ShopifyProvider {
		p, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewDevices(*pool, 5*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	return f, testutil.Isolated(t), provider
}

// F07: a DEFINITIVE failed create (terminal operation error, no remote
// Product) is recovered by a distinct corrected intent — including after
// reopening the database pool — while the identical intent is never
// blindly replayed and the failure history is retained.
func TestR4FailedCreateReceiptRecoversOnCorrectedIntent(t *testing.T) {
	ctx := context.Background()
	f, database, provider := newReceiptStack(t)
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { pool.Close() }()
	poolRef := &pool
	p := provider(poolRef)
	req := upsertRequest("019c0000-0000-7000-8000-0000000000a1", "R4-FAILED", true)
	req.Product.Configurations = []commerce.CommerceConfiguration{
		frameConfig("019c0000-0000-7000-8000-0000000000a2", "classic", "black", "Classic", "Black", 1000, nil, true),
	}
	f.injectOperation = "bundle_operation"
	f.injectMessage = "definitive rejected original choice"
	if _, err := p.UpsertProduct(ctx, req); err == nil || !strings.Contains(err.Error(), "definitive rejected") {
		t.Fatalf("first failed operation not reached: %v", err)
	}
	var state string
	if err := pool.QueryRow(ctx,
		`SELECT async_state FROM commerce_product_mutation_barriers WHERE async_role='bundle_create'`).
		Scan(&state); err != nil || state != "failed" {
		t.Fatalf("failed receipt absent: %s %v", state, err)
	}
	f.injectOperation, f.injectMessage = "", ""

	// The identical create intent is never blindly replayed: its
	// recorded outcome stands and the provider sees no new submission.
	retry := req
	retry.OperationKey = "opkey-retry-same-intent"
	if _, err := p.UpsertProduct(ctx, retry); err == nil || !strings.Contains(err.Error(), "bundle operation failed") {
		t.Fatalf("identical failed intent must return the recorded outcome: %v", err)
	}
	if f.creates != 1 {
		t.Fatalf("identical intent must not resubmit the failed create: creates=%d", f.creates)
	}

	// The original operation is definitively terminal with no Product.
	for _, op := range f.operations {
		op["status"] = "COMPLETE"
		op["product"] = nil
		op["userErrors"] = []any{map[string]any{"message": "original choice rejected"}}
		delete(op, "resource")
	}

	// A DISTINCT corrected intent (new choice content, catalog and
	// configuration revisions, new operation key) is authorized fresh
	// work — including after reopening the real PostgreSQL pool.
	req.OperationKey = "new-corrected-intent"
	req.CatalogRevision++
	req.Product.Configurations[0].StyleNameAR = "صحيح"
	req.Product.Configurations[0].StyleCode = "corrected"
	corrected := "Corrected Style"
	req.Product.Configurations[0].StyleNameEN = &corrected
	req.Product.Configurations[0].ConfigurationRevision++
	pool.Close()
	reopened, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	pool = reopened
	p = provider(poolRef)
	result, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatalf("corrected intent was stranded by the failed receipt: %v", err)
	}
	if f.creates != 2 {
		t.Fatalf("corrected create intent must reach the provider: creates=%d", f.creates)
	}
	if result.ExternalProductID == "" {
		t.Fatal("corrected create must return the sellable identity")
	}
	// History retained: the definitive failure is never erased and the
	// new attempt keeps its own receipt.
	var failed, completed int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_product_mutation_barriers WHERE async_role='bundle_create' AND async_state='failed'`).
		Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("failed receipt history lost: %d %v", failed, err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_product_mutation_barriers WHERE async_role='bundle_create' AND async_state='completed'`).
		Scan(&completed); err != nil || completed != 1 {
		t.Fatalf("corrected attempt receipt missing: %d %v", completed, err)
	}
}

// F20: the sellable bundle parent — not only the hidden base — converges
// canonical content and required activation through public upsert.
func TestR4SellableBundleParentConvergesContentAndActivation(t *testing.T) {
	ctx := context.Background()
	f, database, provider := newReceiptStack(t)
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { pool.Close() }()
	poolRef := &pool
	p := provider(poolRef)
	req := upsertRequest("019c0000-0000-7000-8000-0000000000d1", "R4-CONTENT", true)
	req.Product.Configurations = []commerce.CommerceConfiguration{
		frameConfig("019c0000-0000-7000-8000-0000000000d2", "classic", "black", "Classic", "Black", 1000, nil, true),
	}
	result, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	bundleID := FormatGID(ResourceProduct, result.ExternalProductID)
	if got := f.resources[bundleID]["title"]; got != req.Product.Names[0].Name {
		t.Fatalf("sellable create title not persisted: %v", got)
	}
	if got := f.resources[bundleID]["descriptionHtml"]; got != req.Product.Descriptions["ar"] {
		t.Fatalf("sellable description must converge on every pass: %v", got)
	}

	// Mapped rename/description change converges the sellable parent.
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: result.ExternalProductID}
	req.CatalogRevision++
	req.OperationKey = "changed-content"
	req.Product.Names[0].Name = "عنوان جديد"
	req.Product.Descriptions["ar"] = "وصف جديد"
	if _, err := p.UpsertProduct(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := f.resources[bundleID]["title"]; got != "عنوان جديد" {
		t.Fatalf("stale sellable title after mapped rename: %v", got)
	}
	if got := f.resources[bundleID]["descriptionHtml"]; got != "وصف جديد" {
		t.Fatalf("stale sellable description after mapped change: %v", got)
	}
	// Unrelated merchant fields are never sent: every parent content
	// write is targeted to the owned set only.
	for _, sent := range f.productUpdates {
		for key := range sent {
			if key != "id" && key != "title" && key != "descriptionHtml" && key != "status" {
				t.Fatalf("unrelated merchant field written: %s", key)
			}
		}
	}

	// An explicitly DRAFT owned parent must activate when publication is
	// requested: publication membership alone is not sellable.
	f.resources[bundleID]["status"] = "DRAFT"
	if _, err := p.UpsertProduct(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := f.resources[bundleID]["status"]; got != "ACTIVE" {
		t.Fatalf("published parent left %v: activation must converge", got)
	}

	// Unpublication never drafts the parent and still converges content.
	req.Published = false
	req.OperationKey = "unpublish-pass"
	req.Product.Names[0].Name = "بعد الإلغاء"
	if _, err := p.UpsertProduct(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := f.resources[bundleID]["status"]; got != "ACTIVE" {
		t.Fatalf("unpublication must not draft the parent: %v", got)
	}
	if got := f.resources[bundleID]["title"]; got != "بعد الإلغاء" {
		t.Fatalf("content must keep converging while unpublished: %v", got)
	}
	external, err := ParseGID(bundleID, ResourceProduct)
	if err != nil {
		t.Fatal(err)
	}
	if f.h.published[external] {
		t.Fatal("parent must be unpublished on the configured channel")
	}
}
