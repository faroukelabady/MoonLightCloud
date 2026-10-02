# ADR-0042: Store-Scoped Default Catalog Identity and Adoption Dependency Integrity (Phase 9-R1/R2)

Date: 2026-10-02
Status: accepted (Phase 9-R1; revised Phase 9-R2)

## Context

Retail migration `000001_baseline.up.sql` seeds every fresh installation
with the same fixed reference catalog UUIDs (seven categories such as
`00000000-...-0101` Islamic and five tags such as `10000000-...-0002`
Gold). Cloud 9B treated `catalog_categories.category_id` and
`catalog_tags.tag_id` as globally exclusive aggregate identities with
immutable Store ownership. Two normal fresh Retail Stores therefore
submitted the same seeded IDs under different ingress Stores; the loser
was permanently blocked with `STORE_SCOPE_CONFLICT`, stranding
Product/inventory convergence.

Phase 9-R1 made those IDs a Store-less "shared" namespace. Independent
review proved that model incomplete: the shared row still carried ONE
mutable state and ONE revision counter, so two Stores that independently
renamed or disabled the same default (F07) blocked each other with
`CATALOG_REVISION_CONFLICT` or were silently suppressed; pre-R1
Store-annotated rows still blocked upgrade traffic (F08); and a shared
Category could not be re-parented under a Store-created Category although
frozen Retail accepts that DAG operation (F09). Review also proved (F02)
that legacy adoption validated only outgoing dependencies.

## Decision — Store-scoped default catalog identity (F04/F07/F08/F09)

A default reference UUID is a Store-scoped identity, not a single shared
row:

- Cloud enumerates exactly the baseline seed IDs (`sharedCategoryIDs`,
  `sharedTagIDs` in `store_scope.go`). A **Store-scoped** event for one of
  those IDs is projected under a deterministic per-Store canonical UUID
  (`canonicalDefaultCategoryID`/`canonicalDefaultTagID`,
  `uuid.NewSHA1(NAMESPACE, store+":"+kind+":"+rawID)`). The mapping is a
  fixed constant, so it is stable across rebuilds, restarts, and every
  Cloud instance. Only the *raw* payload ID is tested for membership, so a
  canonical ID never re-maps.
- A **legacy (unbound)** event keeps the raw seeded ID, so pre-existing
  global rows remain addressable and rebuildable.
- Because each Store owns its own physical row (`category_id, store_id`
  unique; migration 25), each Store's independent revision counter orders
  only its own row. Independent A/B edits of the same default therefore
  never block or suppress each other (F07).
- Store-created categories/tags keep the frozen **global aggregate
  identity**: cross-Store same-ID takeover still blocks with
  `STORE_SCOPE_CONFLICT`. Only the enumerated default IDs are Store-scoped.
- Identity derives from the fixed ID + server-derived Store scope, never
  from mutable labels, slugs, or graph position.

### Upgrade from the pre-R2 model (F08)

Migration 25 remains unchanged. Its existing `default_algorithm` column
records 0 for raw rows and 1 for canonical rows. R3 uses 2 for a retired
raw compatibility snapshot: names, identity, source provenance and raw
edges are retained, but its obsolete Store annotation is released in the
same transaction that writes that Store's canonical row. This also frees
the old Store/slug claim. Unbound source history is never assigned a Store.

After deploying R3, run:

```
moonlight-cloud projection recover-catalog
```

Each invocation atomically re-arms at most 100 authoritative source events
for missing canonical defaults and current Category/Product references
still using raw defaults. Run the catalog workers to convergence, inspect
processing errors, then repeat recovery until it reports zero. A nonzero
count means work was queued, not that projection has completed. Durable
pending/retry state survives interruption; rerunning recovery is safe.
Do not rely on a later unrelated catalog edit to complete this transition.

Previously processed sources are included. An obsolete blocked default
requires concrete foreign raw-row provenance and the original ownership
or equal-revision error shape; matching an error code alone is insufficient.
Genuine permanent conflicts and unbound processing remain unchanged.
Existing canonical rows can coexist with raw references during recovery;
equal-revision replay only changes current reference representation when
all other semantics match. The Product graph guard permits that narrow
pending source replay only when the canonical references are valid in the
proposed graph. It still rejects ordinary equal-revision contradictions.

Recovery never rewrites durable payloads or historical Sale/Return data.
Pending/retry events use ordinary projector scheduling. No new migration,
identity algorithm, catalog system, financial logic or dependency is added.
A schema rollback alone cannot undo canonical identities already written;
use the established backup/restore procedure for a lossless rollback.

### Category management compatibility (F09)

Any Category with an effective Store resolves a default parent to
that same Store's canonical row, and resolves a
Store-created parent to itself. A child therefore may sit under a local
root (matching the frozen Retail max-depth-3 DAG), while a foreign
proven-Store parent still blocks. Products resolve their referenced
categories/tags to the same canonical rows, so a Store's Product graph
stays intuitively consistent.

The cross-aggregate orphan guard is evaluated against the **proposed**
graph and only requires a product repair when a referencing product would
*actually* become structurally invalid (its top gains a parent, or a
subcategory becomes unreachable). Both additions and removals run the full integrity guard. A harmless
addition or a removal that keeps every product reachable (e.g. dropping one of several
parents) is accepted — matching the frozen Retail contract that permits
these DAG operations while still rejecting genuine orphaning, cycles, and
max-depth violations.

### Treatment matrix

| Concern | Behavior |
|---|---|
| Fresh installations | Each Store's seeded IDs project to its own canonical rows; products converge; no `STORE_SCOPE_CONFLICT`. |
| Existing installations | Explicit bounded recovery replays processed authoritative sources, releases obsolete raw Store/slug claims atomically and remaps current references. Raw compatibility snapshots are retained. |
| Already projected default rows | No destructive backfill; `default_algorithm` records the scheme. |
| Pending durable catalog events | Unchanged; replay re-derives canonical rows deterministically. |
| Previously blocked events | Only concrete obsolete default-ownership blocks are eligible; genuine permanent conflicts remain blocked. |
| Historical Category/Tag IDs | Sale/return snapshots are immutable and keep raw IDs and labels; reporting is snapshot-based. |
| Product references | Resolve to the same Store's canonical rows; cross-Store references still block. |
| Revisions and replay | Accepted-history arbitration and graph re-evaluation use effective Store scope; a Store's own counter orders its own row; equal-revision identical state is a no-op; divergent same-Store state is a normal conflict. |
| Rebuilds | Wiping current-state rows, resetting the catalog processors, and replaying events reproduces the canonical Store-scoped state deterministically (see `TestR2_RebuildDefaultCatalog`). Projector retry backoff (`DefaultScanInterval` + per-attempt backoff) bounds convergence time; it is a latency, not a correctness, property. |
| Conflicting same-ID attacks | Store-created aggregate identities remain globally arbitrated and still block. Default IDs are public reference data and confer no authority over another Store's row. |

## Decision — complete adoption dependency validation (F02, R1)

An accepted Store transition on a current-state root must not leave a
durable relationship connecting contradictory proven Store ownership:

- Product adoption validates outgoing categories/tags **and** incoming
  inventory, sales policy, and provider mapping ownership.
- Category adoption validates parents, child edges, and referencing
  products; Tag adoption validates attached products.
- The Product projector takes the inventory advisory lock namespace so
  adoption and a concurrent inventory write serialize; SERIALIZABLE
  remains the backstop.
- Availability joins only legacy-NULL or exact same-Store policy/inventory
  **and** requires the product's referenced categories/tags to belong to
  the product's Store; Store-aware publication requires matching
  product/policy ownership. A contradictory state can never expose foreign
  stock/policy as availability or publish it as another Store's resource.

Conflicting transitions fail with the bounded `STORE_SCOPE_CONFLICT` and
roll back atomically. The read checks do not replace the write invariant.

## Migration

- `00025_store_scoped_default_catalog.sql` (append-only): adds
  `default_algorithm SMALLINT NOT NULL DEFAULT 0` to `catalog_categories`
  and `catalog_tags`, and the safety indexes
  `idx_catalog_categories_identity_store` / `idx_catalog_tags_identity_store`
  (`UNIQUE (identity, store_id)`). No existing migration is edited.
- The down migration drops the additive column/indexes. Honest rollback
  limit: canonical Store-scoped rows written under R2 keep their UUIDs; a
  downgrade loses the per-Store default mapping and is **not** lossless.

## Consequences

- Independent Stores manage the default catalog without cross-Store
  interference; same-SKU products remain distinct per Store.
- Foreign inventory/policy can never become positive availability or be
  published under another Store.
- Pre-R1 Store-annotated default projections upgrade via ordinary replay
  plus the bounded recovery command, without an unrelated Store's edit.
- Legacy (unbound) installations keep their raw global rows; historical
  sales keep their sale-time snapshots.
