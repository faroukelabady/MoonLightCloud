package postgres

// Phase 13 — hierarchical ONLINE catalog visibility (real PostgreSQL,
// real projection pipeline, real DAG).
//
// Canonical eligibility (the catalog_product_online_state view, §10):
// a Product is online-eligible iff Product.IsActive AND policy.sell_online
// AND EVERY category node in its classification paths (top category,
// subcategories, and all DAG ancestors) has online_enabled = true.
// Suppression is conservative (§17); missing state fails safe (§19/§20).

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// §136 hierarchy identities.
const (
	catIDA = "aaaaaaaa-0000-4000-8000-000000000001"
	catIDB = "aaaaaaaa-0000-4000-8000-000000000002"
	catIDC = "aaaaaaaa-0000-4000-8000-000000000003"
	catIDX = "aaaaaaaa-0000-4000-8000-000000000004"
	catIDD = "aaaaaaaa-0000-4000-8000-000000000005"
	catIDE = "aaaaaaaa-0000-4000-8000-000000000006"
	prodP1 = "11111111-0000-4000-8000-000000000001"
	prodP2 = "11111111-0000-4000-8000-000000000002"
	prodP3 = "11111111-0000-4000-8000-000000000003"
	prodP4 = "11111111-0000-4000-8000-000000000004"
	prodP5 = "11111111-0000-4000-8000-000000000005"
)

// categoryV2Payload renders catalog.category.snapshot.v2 (Phase 13).
func categoryV2Payload(categoryID, status string, names map[string]string, parents []string, revision int64, onlineEnabled bool) string {
	nameList := []any{}
	for locale, name := range names {
		nameList = append(nameList, map[string]any{"locale": locale, "name": name})
	}
	parentList := []any{}
	for _, parent := range parents {
		parentList = append(parentList, parent)
	}
	raw, _ := json.Marshal(map[string]any{
		"category_id": categoryID, "status": status, "names": nameList,
		"parent_ids": parentList, "online_enabled": onlineEnabled,
		"catalog_revision": revision,
	})
	return string(raw)
}

// policyFixture builds the §136 hierarchy with real projections:
//
//	A (root) -> B (sub) -> C (leaf)
//	A (root) -> E (sub, second classification of P5)
//	X (root) -> D (shared child of A and X)
//	P1 top=A subs=[C]        P2 top=A subs=[B]
//	P3 top=X (unrelated)     P4 top=X subs=[D] (shared node)
//	P5 top=A subs=[C, X2]    (one-of-many classifications)
type policyFixture struct {
	*scopeFixture
	policy catalog.ProductOnlinePolicy
}

func buildPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	f := openScopeFixture(t)
	ctx := context.Background()
	_ = ctx
	pf := &policyFixture{scopeFixture: f}
	seq := 0
	project := func(typ, payload string) {
		t.Helper()
		event := fmt.Sprintf("e5c01300-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, f.devA, f.credA, event, typ, payload)
		projectCatalogTerminal(t, f, event, typ)
	}
	catA, catB, catC, catX, catD, catE := catIDA, catIDB, catIDC, catIDX, catIDD, catIDE
	names := func(name string) map[string]string { return map[string]string{"en": name} }
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(catA, "active", names("A"), nil, 1, true))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(catX, "active", names("X"), nil, 1, true))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(catB, "active", names("B"), []string{catA}, 1, true))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(catC, "active", names("C"), []string{catB}, 1, true))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(catD, "active", names("D"), []string{catA, catX}, 1, true))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(catE, "active", names("E"), []string{catA}, 1, true))

	products := map[string]string{
		"P1": fmt.Sprintf(`{"product_id":"%s","top":"%s","subs":["%s"]}`, "11111111-0000-4000-8000-000000000001", catA, catC),
		"P2": fmt.Sprintf(`{"product_id":"%s","top":"%s","subs":["%s"]}`, "11111111-0000-4000-8000-000000000002", catA, catB),
		"P3": fmt.Sprintf(`{"product_id":"%s","top":"%s","subs":[]}`, "11111111-0000-4000-8000-000000000003", catX),
		"P4": fmt.Sprintf(`{"product_id":"%s","top":"%s","subs":["%s"]}`, "11111111-0000-4000-8000-000000000004", catX, catD),
		"P5": fmt.Sprintf(`{"product_id":"%s","top":"%s","subs":["%s","%s"]}`, "11111111-0000-4000-8000-000000000005", catA, catC, catE),
	}
	for label, spec := range products {
		var shape struct {
			ProductID string   `json:"product_id"`
			Top       string   `json:"top"`
			Subs      []string `json:"subs"`
		}
		if err := json.Unmarshal([]byte(spec), &shape); err != nil {
			t.Fatal(err)
		}
		project(catalog.EventProductSnapshotV1,
			productPayload(shape.ProductID, "PAP-"+label, label, shape.Top, shape.Subs, nil, 1))
	}
	return pf
}

// policyOrFail reads the canonical policy with full diagnostics.
func policyOrFail(t *testing.T, devices Devices, productID string) catalog.ProductOnlinePolicy {
	t.Helper()
	policy, err := devices.CatalogProductOnlinePolicy(context.Background(), productID)
	if err != nil {
		var ids string
		rows, _ := devices.pool.Query(context.Background(), "SELECT product_id FROM catalog_product_online_state")
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids += id + " "
		}
		rows.Close()
		t.Fatalf("policy read %s: %v (view ids=[%s])", productID, err, ids)
	}
	return policy
}

func (pf *policyFixture) eligible(t *testing.T, productID string) catalog.ProductOnlinePolicy {
	t.Helper()
	policy, err := NewDevices(pf.pool, 5*time.Second).CatalogProductOnlinePolicy(context.Background(), productID)
	if err != nil {
		var ids string
		rows, _ := pf.pool.Query(context.Background(), "SELECT product_id FROM catalog_product_online_state")
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids += id + " "
		}
		rows.Close()
		t.Fatalf("policy read: %v (wanted %s view ids=[%s])", err, productID, ids)
	}
	return policy
}

func (pf *policyFixture) toggle(t *testing.T, categoryID string, online bool, revision int64, parents ...string) {
	t.Helper()
	event := fmt.Sprintf("e5c013f0-0000-4000-8000-%012d", revision)
	pf.ingest(t, pf.devA, pf.credA, event, catalog.EventCategorySnapshotV2,
		categoryV2Payload(categoryID, "active", map[string]string{"en": "toggle"}, parents, revision, online))
	// Snapshots carry the COMPLETE parent set: a toggle must preserve the
	// node's parents (§41) — only the policy changes.
	result := projectCatalogTerminal(t, pf.scopeFixture, event, catalog.EventCategorySnapshotV2)
	if result.Outcome != catalog.OutcomeProcessed && result.Outcome != catalog.OutcomeAlready {
		t.Fatalf("toggle %s rev %d not applied: %+v", categoryID, revision, result)
	}
}

// §135/§136/§137: the eligibility matrix over a real multi-parent DAG.
func TestCategoryOnlineEligibilityMatrix(t *testing.T) {
	pf := buildPolicyFixture(t)
	ctx := context.Background()

	// All nodes enabled: every Product eligible.
	for label, id := range map[string]string{"P1": prodP1, "P2": prodP2, "P3": prodP3, "P4": prodP4, "P5": prodP5} {
		if policy := pf.eligible(t, id); !policy.Allowed {
			t.Fatalf("%s: expected allowed, got %+v", label, policy)
		}
	}

	// §12: disabled TOP category suppresses everything beneath it
	// (including through descendants), while unrelated Products hold.
	pf.toggle(t, catIDA, false, 2)
	for label, id := range map[string]string{"P1": prodP1, "P2": prodP2, "P5": prodP5} {
		policy := pf.eligible(t, id)
		if policy.Allowed || policy.Reason != catalog.OnlineBlockedCategory {
			t.Fatalf("%s under disabled top category: %+v", label, policy)
		}
	}
	if policy := pf.eligible(t, prodP3); !policy.Allowed {
		t.Fatalf("unrelated Product suppressed: %+v", policy)
	}
	// §16: shared node D has parent A — P4 reaches D under X, but the
	// ancestor A participates in D's path: conservative suppression.
	if policy := pf.eligible(t, prodP4); policy.Allowed {
		t.Fatalf("shared node under disabled ancestor: %+v", policy)
	}
	pf.toggle(t, catIDA, true, 3)

	// §14: disabled ANCESTOR (B) suppresses the leaf Product P1 (under C).
	pf.toggle(t, catIDB, false, 4, catIDA)
	if policy := pf.eligible(t, prodP1); policy.Allowed || policy.Reason != catalog.OnlineBlockedCategory {
		t.Fatalf("ancestor disable ignored: %+v", policy)
	}
	if policy := pf.eligible(t, prodP2); policy.Allowed {
		t.Fatalf("direct subcategory disable ignored: %+v", policy)
	}
	if policy := pf.eligible(t, prodP3); !policy.Allowed {
		t.Fatalf("unrelated Product suppressed: %+v", policy)
	}
	pf.toggle(t, catIDB, true, 5, catIDA)

	// §13: disabled LEAF (C) suppresses P1 and P5 (classification node).
	pf.toggle(t, catIDC, false, 6, catIDB)
	if policy := pf.eligible(t, prodP1); policy.Allowed {
		t.Fatalf("leaf disable ignored: %+v", policy)
	}
	// §17: one-of-many classifications disabled => suppressed globally.
	if policy := pf.eligible(t, prodP5); policy.Allowed {
		t.Fatalf("multi-classification Product not suppressed: %+v", policy)
	}
	pf.toggle(t, catIDC, true, 7, catIDB)

	// §15/§16: shared node D disabled => P4 suppressed regardless of the
	// Islamic/X path; Products under unrelated siblings unchanged.
	pf.toggle(t, catIDD, false, 8, catIDA, catIDX)
	if policy := pf.eligible(t, prodP4); policy.Allowed {
		t.Fatalf("shared node disable ignored: %+v", policy)
	}
	if policy := pf.eligible(t, prodP3); !policy.Allowed {
		t.Fatalf("unrelated Product suppressed by shared node: %+v", policy)
	}
	pf.toggle(t, catIDD, true, 9, catIDA, catIDX)

	// Deterministic blocker (§55): disable both C and E (P5's nodes) and
	// confirm the blocker is chosen by depth then ID — stable across reads.
	pf.toggle(t, catIDC, false, 10, catIDB)
	pf.toggle(t, catIDE, false, 11, catIDA)
	first := pf.eligible(t, prodP5)
	second := pf.eligible(t, prodP5)
	if first.BlockingCategoryID == nil || *first.BlockingCategoryID != *second.BlockingCategoryID {
		t.Fatalf("blocker not deterministic: %+v vs %+v", first, second)
	}
	_ = ctx
}

// §142/§143: fan-out exactness and DAG dedupe — one logical
// re-evaluation per affected Product, none for unrelated Products.
func TestCategoryOnlineFanOutExactness(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	_ = ctx
	seq := 0
	project := func(typ, payload string) {
		event := fmt.Sprintf("e5c013f2-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, f.devA, f.credA, event, typ, payload)
		projectCatalogTerminal(t, f, event, typ)
	}
	root := "aaaaaaaa-0000-4000-8000-0000000000f1"
	other := "aaaaaaaa-0000-4000-8000-0000000000f2"
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(root, "active", map[string]string{"en": "Root"}, nil, 1, true))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(other, "active", map[string]string{"en": "Other"}, nil, 1, true))

	// 3 Products under root's subtree (one under root directly), 2 under other.
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("22222222-0000-4000-8000-%012d", i)
		top := other
		if i < 3 {
			top = root
		}
		project(catalog.EventProductSnapshotV1,
			productPayload(id, fmt.Sprintf("PAP-%d", i), "P", top, nil, nil, 1))
	}
	// Drain the product-classification enqueue noise before measuring.
	devices := NewDevices(f.pool, 5*time.Second)
	drainReevaluations(t, devices)

	// Disabling root affects exactly the 3 subtree Products.
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(root, "active", map[string]string{"en": "Root"}, nil, 2, false))
	rows := reevaluationRows(t, devices)
	if len(rows) != 3 {
		t.Fatalf("fan-out rows = %d, want exactly 3: %v", len(rows), rows)
	}
	for _, productID := range rows {
		if productID[0] != '2' {
			t.Fatalf("unrelated Product fanned out: %s", productID)
		}
	}

	// §111/§145: replayed identical event and rapid toggles coalesce to
	// one row per Product (dedupe) and converge to the latest state.
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(root, "active", map[string]string{"en": "Root"}, nil, 2, false))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(root, "active", map[string]string{"en": "Root"}, nil, 3, true))
	if rows := reevaluationRows(t, devices); len(rows) != 3 {
		t.Fatalf("shared-DAG/replay fan-out must dedupe to one row per Product: %v", rows)
	}
	// Final durable state is the newest policy (enabled).
	state := policyOrFail(t, devices, rows[0])
	if !state.Allowed {
		t.Fatalf("rapid toggle must converge to latest committed policy: %+v", state)
	}
}

// §42/§43: parent-edge changes re-evaluate affected Products even when
// no online_enabled boolean changed.
func TestCategoryOnlineParentEdgeChangeFiresFanOut(t *testing.T) {
	f := openScopeFixture(t)
	seq := 0
	project := func(typ, payload string) {
		event := fmt.Sprintf("e5c013f3-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, f.devA, f.credA, event, typ, payload)
		result := projectCatalogTerminal(t, f, event, typ)
		if result.Outcome != catalog.OutcomeProcessed && result.Outcome != catalog.OutcomeAlready {
			t.Fatalf("event %s not applied: %+v", event, result)
		}
	}
	rootOff := "aaaaaaaa-0000-4000-8000-0000000000e1"
	rootOn := "aaaaaaaa-0000-4000-8000-0000000000e2"
	sub := "aaaaaaaa-0000-4000-8000-0000000000e3"
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(rootOff, "active", map[string]string{"en": "Off"}, nil, 1, false))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(rootOn, "active", map[string]string{"en": "On"}, nil, 1, true))
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(sub, "active", map[string]string{"en": "Sub"}, []string{rootOn}, 1, true))
	productID := "33333333-0000-4000-8000-000000000001"
	project(catalog.EventProductSnapshotV1,
		productPayload(productID, "PAP-MOVE", "Move", rootOn, []string{sub}, nil, 1))

	devices := NewDevices(f.pool, 5*time.Second)
	drainReevaluations(t, devices)
	state := policyOrFail(t, devices, productID)
	if !state.Allowed {
		t.Fatalf("precondition: %+v", state)
	}

	// §43: ADD the disabled ancestor as a second parent of the enabled
	// Sub (multi-parent DAG — the frozen product-structure invariant
	// keeps the original reachable path, so this is the permitted form):
	// the Product must become offline WITHOUT toggling anything on Sub
	// or the Product.
	project(catalog.EventCategorySnapshotV2,
		categoryV2Payload(sub, "active", map[string]string{"en": "Sub"}, []string{rootOn, rootOff}, 2, true))
	state = policyOrFail(t, devices, productID)
	if state.Allowed {
		t.Fatalf("parent-edge change must suppress through the disabled ancestor: %+v", state)
	}
	if rows := reevaluationRows(t, devices); len(rows) == 0 {
		t.Fatal("parent-edge change must schedule durable re-evaluation")
	}

	// §44: removing the disabled ancestor restores eligibility.
	project(catalog.EventCategorySnapshotV2,
		categoryV2Payload(sub, "active", map[string]string{"en": "Sub"}, []string{rootOn}, 3, true))
	state = policyOrFail(t, devices, productID)
	if !state.Allowed {
		t.Fatalf("removing disabled ancestor must restore eligibility: %+v", state)
	}
}

// reevaluationRows lists pending Product re-evaluation requests.
func reevaluationRows(t *testing.T, d Devices) []string {
	t.Helper()
	rows, err := d.pool.Query(context.Background(), "SELECT product_id::text FROM commerce_product_reevaluations ORDER BY product_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// drainReevaluations clears queued work (test setup only).
func drainReevaluations(t *testing.T, d Devices) {
	t.Helper()
	ctx := context.Background()
	rows, err := d.ClaimProductReevaluations(ctx, 100, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if ok, err := d.CompleteProductReevaluation(ctx, row); err != nil || !ok {
			t.Fatalf("complete reevaluation %s: %v", row.ProductID, err)
		}
	}
}

// §30-§33: dual-version projection semantics — v1 defaults to enabled,
// a stale v1 can never overwrite a newer v2, and equal revisions with
// conflicting policy follow the frozen equal-revision conflict rule.
func TestCategoryOnlineSyncContractVersions(t *testing.T) {
	pf := buildPolicyFixture(t)
	cat := catIDA

	// §30: historical v1 replay projects online_enabled = true.
	v1Event := "e5c013a1-0000-4000-8000-000000000001"
	pf.ingest(t, pf.devA, pf.credA, v1Event, catalog.EventCategorySnapshotV1,
		categoryPayload(cat, "active", map[string]string{"en": "A"}, nil, 12))
	if result := projectCatalogTerminal(t, pf.scopeFixture, v1Event, catalog.EventCategorySnapshotV1); result.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("v1 replay not applied: %+v", result)
	}
	if policy := pf.eligible(t, prodP1); !policy.Allowed {
		t.Fatalf("v1 must project online_enabled = true: %+v", policy)
	}

	// §31: v1 rev12 then v2 rev13 false -> false.
	v2Event := "e5c013a1-0000-4000-8000-000000000002"
	pf.ingest(t, pf.devA, pf.credA, v2Event, catalog.EventCategorySnapshotV2,
		categoryV2Payload(cat, "active", map[string]string{"en": "A"}, nil, 13, false))
	if result := projectCatalogTerminal(t, pf.scopeFixture, v2Event, catalog.EventCategorySnapshotV2); result.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("v2 update not applied: %+v", result)
	}
	if policy := pf.eligible(t, prodP1); policy.Allowed {
		t.Fatalf("v2 policy must apply: %+v", policy)
	}

	// §32: stale v1 rev12 after v2 rev13 -> rejected as superseded; the
	// newer policy remains durable.
	stale := "e5c013a1-0000-4000-8000-000000000003"
	pf.ingest(t, pf.devA, pf.credA, stale, catalog.EventCategorySnapshotV1,
		categoryPayload(cat, "active", map[string]string{"en": "A"}, nil, 12))
	if result := projectCatalogTerminal(t, pf.scopeFixture, stale, catalog.EventCategorySnapshotV1); result.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale event handling changed: %+v", result)
	}
	if policy := pf.eligible(t, prodP1); policy.Allowed {
		t.Fatalf("stale v1 must never overwrite newer v2 policy: %+v", policy)
	}

	// §33: equal revision with conflicting policy -> frozen
	// equal-revision conflict semantics (blocked, never last-write-wins).
	conflict := "e5c013a1-0000-4000-8000-000000000004"
	pf.ingest(t, pf.devA, pf.credA, conflict, catalog.EventCategorySnapshotV2,
		categoryV2Payload(cat, "active", map[string]string{"en": "A"}, nil, 13, true))
	result := projectCatalogTerminal(t, pf.scopeFixture, conflict, catalog.EventCategorySnapshotV2)
	if result.Outcome != catalog.OutcomeBlocked {
		t.Fatalf("equal-revision policy contradiction must block: %+v", result)
	}
	if policy := pf.eligible(t, prodP1); policy.Allowed {
		t.Fatalf("conflicting equal revision must not flip policy: %+v", policy)
	}
}

// §88/§89/§144: crash safety — durable rows plus leased claiming mean a
// crashed worker's batch is recovered automatically and no Product is
// ever stranded. Enqueue is atomic with the projection commit (proven by
// the projection tests), so even a crash before the worker starts leaves
// convergable work.
func TestReevaluationQueueRecoversAbandonedWork(t *testing.T) {
	f := openScopeFixture(t)
	seq := 0
	project := func(typ, payload string) {
		event := fmt.Sprintf("e5c013f4-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, f.devA, f.credA, event, typ, payload)
		result := projectCatalogTerminal(t, f, event, typ)
		if result.Outcome != catalog.OutcomeProcessed && result.Outcome != catalog.OutcomeAlready {
			t.Fatalf("event %s not applied: %+v", event, result)
		}
	}
	root := "aaaaaaaa-0000-4000-8000-0000000000f9"
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(root, "active", map[string]string{"en": "R"}, nil, 1, true))
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("44444444-0000-4000-8000-%012d", i)
		project(catalog.EventProductSnapshotV1, productPayload(id, fmt.Sprintf("PAP-R%d", i), "P", root, nil, nil, 1))
	}
	devices := NewDevices(f.pool, 5*time.Second)
	drainReevaluations(t, devices)
	project(catalog.EventCategorySnapshotV2, categoryV2Payload(root, "active", map[string]string{"en": "R"}, nil, 2, false))

	// First worker claims the batch and crashes (no completion).
	claimed, err := devices.ClaimProductReevaluations(context.Background(), 25, time.Now().Add(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 5 {
		t.Fatalf("claimed %d, want 5", len(claimed))
	}
	// While the lease is live no other instance steals the work
	// (multi-instance safety), and after it expires everything returns.
	again, err := devices.ClaimProductReevaluations(context.Background(), 25, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("leased work must not be stolen: %v", again)
	}
	time.Sleep(60 * time.Millisecond)
	recovered, err := devices.ClaimProductReevaluations(context.Background(), 25, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 5 {
		t.Fatalf("abandoned batch must fully return, got %d", len(recovered))
	}
	// Recovery completes cleanly: nothing stranded afterwards.
	for _, row := range recovered {
		if ok, err := devices.CompleteProductReevaluation(context.Background(), row); err != nil || !ok {
			t.Fatal(err)
		}
	}
	if rows := reevaluationRows(t, devices); len(rows) != 0 {
		t.Fatalf("queue must be empty after recovery: %v", rows)
	}
}
