package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// Both projection verdict and durable revision must be independent of a
// foreign default stream, including accepted events without processing rows.
func TestR3_DefaultRevisionArbitration(t *testing.T) {
	for _, typ := range []string{catalog.EventTagSnapshotV1, catalog.EventCategorySnapshotV1} {
		for _, state := range []string{"missing", "pending", "retry", "processed"} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/reverse=%v", typ, state, reverse), func(t *testing.T) {
					f := openScopeFixture(t)
					ctx := context.Background()
					da, ca, db, cb, sa, sb := f.devA, f.credA, f.devB, f.credB, scopeStoreA, scopeStoreB
					if reverse {
						da, ca, db, cb, sa, sb = db, cb, da, ca, sb, sa
					}
					id, table, key, processor := sharedTagGold, "catalog_tags", "tag_id", catalog.ProcessorTagProjectionV1
					payload := func(name string, rev int64) string {
						return tagPayload(id, "gold", true, map[string]string{"en": name}, rev)
					}
					if typ == catalog.EventCategorySnapshotV1 {
						id, table, key, processor = sharedCatIslamic, "catalog_categories", "category_id", catalog.ProcessorCategoryProjectionV1
						payload = func(name string, rev int64) string {
							return categoryPayload(id, "active", map[string]string{"en": name}, nil, rev)
						}
					}
					for i, d := range []struct{ dev, cred string }{{da, ca}, {db, cb}} {
						e := r2EventID(700 + i)
						f.ingest(t, d.dev, d.cred, e, typ, payload("initial", 1))
						requireOutcome(t, r3Project(t, f, e, typ), catalog.OutcomeProcessed, "")
					}
					newer := r2EventID(702)
					f.ingest(t, da, ca, newer, typ, payload("foreign 3", 3))
					switch state {
					case "processed":
						requireOutcome(t, r3Project(t, f, newer, typ), catalog.OutcomeProcessed, "")
					case "pending", "retry":
						if _, err := f.pool.Exec(ctx, `INSERT INTO sync_event_processing(event_id,processor,status,attempt_count) VALUES($1,$2,$3,1)`, newer, processor, state); err != nil {
							t.Fatal(err)
						}
					}
					own := r2EventID(703)
					f.ingest(t, db, cb, own, typ, payload("own 2", 2))
					requireOutcome(t, r3Project(t, f, own, typ), catalog.OutcomeProcessed, "")
					physical := scopedTagID(t, id, sb)
					if typ == catalog.EventCategorySnapshotV1 {
						physical = scopedCatID(t, id, sb)
					}
					var rev int64
					var name, status string
					if err := f.pool.QueryRow(ctx, fmt.Sprintf(`SELECT source_revision,name_en FROM %s WHERE %s=$1`, table, key), physical).Scan(&rev, &name); err != nil {
						t.Fatal(err)
					}
					if rev != 2 || name != "own 2" {
						t.Fatalf("silently suppressed own update: rev=%d name=%s", rev, name)
					}
					if err := f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, own, processor).Scan(&status); err != nil || status != "processed" {
						t.Fatalf("own verdict %s: %v", status, err)
					}

					if state != "processed" {
						queuedOlder := r2EventID(706)
						f.ingest(t, da, ca, queuedOlder, typ, payload("queued old 2", 2))
						requireOutcome(t, r3Project(t, f, queuedOlder, typ), catalog.OutcomeProcessed, "")
						ownPhysical := scopedTagID(t, id, sa)
						if typ == catalog.EventCategorySnapshotV1 {
							ownPhysical = scopedCatID(t, id, sa)
						}
						if err := f.pool.QueryRow(ctx, fmt.Sprintf(`SELECT source_revision FROM %s WHERE %s=$1`, table, key), ownPhysical).Scan(&rev); err != nil || rev != 1 {
							t.Fatalf("same-stream supersession did not retain revision1: %d %v", rev, err)
						}
					}
					if state != "processed" {
						expireBackoff(t, f, newer)
						requireOutcome(t, r3Project(t, f, newer, typ), catalog.OutcomeProcessed, "")
					}
					// Same-stream supersession remains intentional, and contradictory
					// equal revisions must never replace the durable state.
					older := r2EventID(704)
					f.ingest(t, da, ca, older, typ, payload("old 2", 2))
					requireOutcome(t, r3Project(t, f, older, typ), catalog.OutcomeProcessed, "")
					conflict := r2EventID(705)
					f.ingest(t, da, ca, conflict, typ, payload("contradiction", 3))
					requireOutcome(t, r3Project(t, f, conflict, typ), catalog.OutcomeBlocked, ErrCatalogRevisionConflict)
					physical = scopedTagID(t, id, sa)
					if typ == catalog.EventCategorySnapshotV1 {
						physical = scopedCatID(t, id, sa)
					}
					if err := f.pool.QueryRow(ctx, fmt.Sprintf(`SELECT source_revision,name_en FROM %s WHERE %s=$1`, table, key), physical).Scan(&rev, &name); err != nil || rev != 3 || name != "foreign 3" {
						t.Fatalf("same-stream state %d %s: %v", rev, name, err)
					}
				})
			}
		}
	}
}

func TestR3_CategoryParentCompatibility(t *testing.T) {
	for _, defaultChild := range []bool{false, true} {
		for _, defaultParent := range []bool{false, true} {
			t.Run(fmt.Sprintf("child=%v/parent=%v", defaultChild, defaultParent), func(t *testing.T) {
				f := openScopeFixture(t)
				ctx := context.Background()
				parent := "a9000000-0000-4000-8000-000000000001"
				child := "a9000000-0000-4000-8000-000000000002"
				if defaultParent {
					parent = sharedCatIslamic
				}
				if defaultChild {
					child = "00000000-0000-0000-0000-000000000202"
				}
				childPayload := categoryPayload(child, "active", map[string]string{"ar": "فرع", "en": "child"}, []string{parent}, 1)
				f.ingest(t, f.devA, f.credA, r2EventID(710), catalog.EventCategorySnapshotV1, childPayload)
				requireOutcome(t, r3Project(t, f, r2EventID(710), catalog.EventCategorySnapshotV1), catalog.OutcomeRetryable, ErrCatalogDependencyWait)
				f.ingest(t, f.devA, f.credA, r2EventID(711), catalog.EventCategorySnapshotV1, categoryPayload(parent, "active", map[string]string{"en": "root"}, nil, 1))
				requireOutcome(t, r3Project(t, f, r2EventID(711), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				expireBackoff(t, f, r2EventID(710))
				requireOutcome(t, r3Project(t, f, r2EventID(710), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				physicalParent, physicalChild := parent, child
				if defaultParent {
					physicalParent = scopedCatID(t, parent, scopeStoreA)
				}
				if defaultChild {
					physicalChild = scopedCatID(t, child, scopeStoreA)
				}
				var exists bool
				if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_category_edges WHERE parent_id=$1 AND child_id=$2)`, physicalParent, physicalChild).Scan(&exists); err != nil || !exists {
					t.Fatalf("canonical edge missing: %v", err)
				}
				f.ingest(t, f.devA, f.credA, r2EventID(712), catalog.EventCategorySnapshotV1, childPayload)
				requireOutcome(t, r3Project(t, f, r2EventID(712), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				// Add a second local parent without dropping the first parent or order.
				second := "a9000000-0000-4000-8000-000000000003"
				f.ingest(t, f.devA, f.credA, r2EventID(713), catalog.EventCategorySnapshotV1, categoryPayload(second, "active", map[string]string{"en": "second"}, nil, 1))
				requireOutcome(t, r3Project(t, f, r2EventID(713), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				f.ingest(t, f.devA, f.credA, r2EventID(714), catalog.EventCategorySnapshotV1, categoryPayload(child, "active", map[string]string{"ar": "فرع", "en": "child"}, []string{parent, second}, 2))
				requireOutcome(t, r3Project(t, f, r2EventID(714), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				foreign := "b9000000-0000-4000-8000-000000000001"
				f.ingest(t, f.devB, f.credB, r2EventID(715), catalog.EventCategorySnapshotV1, categoryPayload(foreign, "active", map[string]string{"en": "foreign"}, nil, 1))
				requireOutcome(t, r3Project(t, f, r2EventID(715), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				f.ingest(t, f.devA, f.credA, r2EventID(716), catalog.EventCategorySnapshotV1, categoryPayload(child, "active", map[string]string{"ar": "فرع", "en": "child"}, []string{foreign}, 3))
				requireOutcome(t, r3Project(t, f, r2EventID(716), catalog.EventCategorySnapshotV1), catalog.OutcomeBlocked, ErrStoreScopeConflict)
			})
		}
	}
}

func TestR3_ParentAdditionProtectsProductTop(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprintf("repair=%v", repair), func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			root := "a9000000-0000-4000-8000-000000000011"
			parent := "a9000000-0000-4000-8000-000000000012"
			product := "a9000000-0000-4000-8000-000000000013"
			for i, id := range []string{root, parent} {
				e := r2EventID(720 + i)
				f.ingest(t, f.devA, f.credA, e, catalog.EventCategorySnapshotV1, categoryPayload(id, "active", map[string]string{"en": "root"}, nil, 1))
				requireOutcome(t, r3Project(t, f, e, catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
			}
			f.ingest(t, f.devA, f.credA, r2EventID(722), catalog.EventProductSnapshotV1, productPayload(product, "R3-TOP", "product", root, nil, nil, 1))
			requireOutcome(t, r3Project(t, f, r2EventID(722), catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
			if repair {
				f.ingest(t, f.devA, f.credA, r2EventID(723), catalog.EventProductSnapshotV1, productPayload(product, "R3-TOP", "product", parent, []string{root}, nil, 2))
			}
			f.ingest(t, f.devA, f.credA, r2EventID(724), catalog.EventCategorySnapshotV1, categoryPayload(root, "active", map[string]string{"en": "root"}, []string{parent}, 2))
			res := r3Project(t, f, r2EventID(724), catalog.EventCategorySnapshotV1)
			if repair {
				requireOutcome(t, res, catalog.OutcomeProcessed, "")
				requireOutcome(t, r3Project(t, f, r2EventID(723), catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
			} else {
				requireOutcome(t, res, catalog.OutcomeBlocked, ErrCatalogGraphConflict)
			}
			var edges int
			var top string
			var rev int64
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_category_edges WHERE child_id=$1`, root).Scan(&edges); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(ctx, `SELECT top_category_id::text FROM catalog_products WHERE product_id=$1`, product).Scan(&top); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(ctx, `SELECT source_revision FROM catalog_categories WHERE category_id=$1`, root).Scan(&rev); err != nil {
				t.Fatal(err)
			}
			if repair {
				if edges != 1 || top != parent || rev != 2 {
					t.Fatalf("repair state edges=%d top=%s rev=%d", edges, top, rev)
				}
			} else if edges != 0 || top != root || rev != 1 {
				t.Fatalf("rollback state edges=%d top=%s rev=%d", edges, top, rev)
			}
		})
	}
}

func r3Project(t *testing.T, f *scopeFixture, id, typ string) catalog.ProjectResult {
	t.Helper()
	result := catalogWorkerAttempt(NewDevices(f.pool, 5*time.Second), id, typ)
	if result.Err != nil && !catalogAttemptRetryable(result.Result, result.Err) {
		reportCatalogWorkerError(t, result.Err)
	}
	return result.Result
}

// Parent additions to subcategories and redundant removals are harmless
// when the Product's root/reachability remain valid. Labels never require
// an accepted Product repair; the final destructive removal does.
func TestR3_HarmlessParentChanges(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	root := "a9000000-0000-4000-8000-000000000041"
	mid := "a9000000-0000-4000-8000-000000000042"
	child := "a9000000-0000-4000-8000-000000000043"
	product := "a9000000-0000-4000-8000-000000000044"
	seq := 740
	project := func(typ, payload string) catalog.ProjectResult {
		seq++
		e := r2EventID(seq)
		f.ingest(t, f.devA, f.credA, e, typ, payload)
		return r3Project(t, f, e, typ)
	}
	names := map[string]string{"ar": "فئة", "en": "category"}
	requireOutcome(t, project(catalog.EventCategorySnapshotV1, categoryPayload(root, "active", names, nil, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(catalog.EventCategorySnapshotV1, categoryPayload(mid, "active", names, []string{root}, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(catalog.EventCategorySnapshotV1, categoryPayload(child, "active", names, []string{root}, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(catalog.EventProductSnapshotV1, productPayload(product, "R3-GRAPH", "product", root, []string{child}, nil, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(catalog.EventCategorySnapshotV1, categoryPayload(child, "active", names, []string{root, mid}, 2)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(catalog.EventCategorySnapshotV1, categoryPayload(child, "active", names, []string{mid}, 3)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(catalog.EventCategorySnapshotV1, categoryPayload(child, "hidden", map[string]string{"ar": "جديد", "en": "new"}, []string{mid}, 4)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(catalog.EventCategorySnapshotV1, categoryPayload(child, "hidden", map[string]string{"ar": "جديد", "en": "new"}, nil, 5)), catalog.OutcomeBlocked, ErrCatalogGraphConflict)
	var parent string
	var rev int64
	if err := f.pool.QueryRow(ctx, `SELECT parent_id::text,c.source_revision FROM catalog_category_edges e JOIN catalog_categories c ON c.category_id=e.child_id WHERE e.child_id=$1`, child).Scan(&parent, &rev); err != nil || parent != mid || rev != 4 {
		t.Fatalf("invalid removal committed: %s %d %v", parent, rev, err)
	}
}

func TestR3_ParentAdditionRacingProduct(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		t.Run(fmt.Sprint(iteration), func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			root := "a9000000-0000-4000-8000-000000000051"
			parent := "a9000000-0000-4000-8000-000000000052"
			product := "a9000000-0000-4000-8000-000000000053"
			for i, id := range []string{root, parent} {
				e := r2EventID(760 + i)
				f.ingest(t, f.devA, f.credA, e, catalog.EventCategorySnapshotV1, categoryPayload(id, "active", map[string]string{"ar": "فئة", "en": "root"}, nil, 1))
				requireOutcome(t, r3Project(t, f, e, catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
			}
			f.ingest(t, f.devA, f.credA, r2EventID(762), catalog.EventProductSnapshotV1, productPayload(product, "R3-RACE", "product", root, nil, nil, 1))
			f.ingest(t, f.devA, f.credA, r2EventID(763), catalog.EventCategorySnapshotV1, categoryPayload(root, "active", map[string]string{"ar": "فئة", "en": "root"}, []string{parent}, 2))
			var wg sync.WaitGroup
			for _, entry := range []struct{ id, typ string }{{r2EventID(762), catalog.EventProductSnapshotV1}, {r2EventID(763), catalog.EventCategorySnapshotV1}} {
				wg.Add(1)
				go func(id, typ string) {
					defer wg.Done()
					result := runCatalogWorker(NewDevices(f.pool, 5*time.Second), id, typ, catalogRetryBudget)
					reportCatalogWorkerError(t, result.Err)
				}(entry.id, entry.typ)
			}
			wg.Wait()
			var invalid bool
			if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_products p JOIN catalog_category_edges e ON e.child_id=p.top_category_id WHERE p.product_id=$1)`, product).Scan(&invalid); err != nil || invalid {
				t.Fatalf("invalid durable root assignment: %v", err)
			}
			var unresolved int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM sync_event_processing WHERE event_id IN ($1,$2) AND status NOT IN ('processed','blocked')`, r2EventID(762), r2EventID(763)).Scan(&unresolved); err != nil || unresolved != 0 {
				t.Fatalf("unsettled race=%d %v", unresolved, err)
			}
		})
	}
}

func TestR3_DefaultGraphRepairArbitration(t *testing.T) {
	for _, ownRepair := range []bool{false, true} {
		t.Run(fmt.Sprint(ownRepair), func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			parent := "a9000000-0000-4000-8000-000000000061"
			product := "a9000000-0000-4000-8000-000000000062"
			names := map[string]string{"ar": "فئة", "en": "category"}
			f.ingest(t, f.devA, f.credA, r2EventID(780), catalog.EventCategorySnapshotV1, categoryPayload(parent, "active", names, nil, 1))
			requireOutcome(t, r3Project(t, f, r2EventID(780), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
			f.ingest(t, f.devA, f.credA, r2EventID(781), catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", names, []string{parent}, 1))
			requireOutcome(t, r3Project(t, f, r2EventID(781), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
			dev, cred := f.devB, f.credB
			if ownRepair {
				dev, cred = f.devA, f.credA
			}
			f.ingest(t, dev, cred, r2EventID(782), catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", names, nil, 2))
			f.ingest(t, f.devA, f.credA, r2EventID(783), catalog.EventProductSnapshotV1, productPayload(product, "R3-REPAIR", "product", sharedCatIslamic, nil, nil, 1))
			res := r3Project(t, f, r2EventID(783), catalog.EventProductSnapshotV1)
			if ownRepair {
				requireOutcome(t, res, catalog.OutcomeRetryable, ErrCatalogDependencyWait)
				requireOutcome(t, r3Project(t, f, r2EventID(782), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				expireBackoff(t, f, r2EventID(783))
				requireOutcome(t, r3Project(t, f, r2EventID(783), catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
			} else {
				requireOutcome(t, res, catalog.OutcomeBlocked, ErrCatalogInvalidRelation)
				requireOutcome(t, r3Project(t, f, r2EventID(782), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
				// A foreign graph advancement cannot re-arm the blocked local event.
				if err := NewDevices(f.pool, 5*time.Second).RearmBlockedProducts(ctx); err != nil {
					t.Fatal(err)
				}
				var status string
				if err := f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, r2EventID(783), catalog.ProcessorProductProjectionV1).Scan(&status); err != nil || status != "blocked" {
					t.Fatalf("foreign graph repaired local event: %s %v", status, err)
				}
			}
		})
	}
}
