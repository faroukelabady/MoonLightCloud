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

Migration 25 adds `default_algorithm` (0 = raw ID, 1 = Store-scoped
canonical ID) with a `DEFAULT 0`, so pre-existing rows are explicitly
marked "raw". A default-identity event computes its canonical row directly
and ignores any pre-R2 Store annotation on the raw row (the frozen
ownership gate is used only for legacy and Store-created identities). An
equal-revision Store-scoped event whose canonical row is absent
re-projects rather than conflicting, so no "unrelated A edit" is required.

Permanently blocked catalog events from the obsolete model are recovered
with the bounded operator command:

```
moonlight-cloud projection recover-catalog
```

which flips only `STORE_SCOPE_CONFLICT` / `CATALOG_REVISION_CONFLICT`
blocks on the three catalog processors back to pending. It never resets
validation, cycle, depth, graph-conflict, inventory/policy, sale, or
return failures, and never rewrites a durable event.

### Category management compatibility (F09)

A Store-scoped default Category resolves a Store-scoped default parent to
the *parent Store's* canonical row for the same Store, and resolves a
Store-created parent to itself. A child therefore may sit under a local
root (matching the frozen Retail max-depth-3 DAG), while a foreign
proven-Store parent still blocks. Products resolve their referenced
categories/tags to the same canonical rows, so a Store's Product graph
stays intuitively consistent.

The cross-aggregate orphan guard is evaluated against the **proposed**
graph and only requires a product repair when a referencing product would
*actually* become structurally invalid (its top gains a parent, or a
subcategory becomes unreachable). A pure parent addition never blocks, and
a removal that keeps every product reachable (e.g. dropping one of several
parents) is accepted — matching the frozen Retail contract that permits
these DAG operations while still rejecting genuine orphaning, cycles, and
max-depth violations.

### Treatment matrix

| Concern | Behavior |
|---|---|
| Fresh installations | Each Store's seeded IDs project to its own canonical rows; products converge; no `STORE_SCOPE_CONFLICT`. |
| Existing installations | Raw pre-R2 rows keep their data and annotation; the first Store-scoped event writes the canonical row. Raw rows are never deleted. |
| Already projected default rows | No destructive backfill; `default_algorithm` records the scheme. |
| Pending durable catalog events | Unchanged; replay re-derives canonical rows deterministically. |
| Previously blocked events | `projection recover-catalog` re-arms only identity/scope blocks. |
| Historical Category/Tag IDs | Sale/return snapshots are immutable and keep raw IDs and labels; reporting is snapshot-based. |
| Product references | Resolve to the same Store's canonical rows; cross-Store references still block. |
| Revisions and replay | A Store's own counter orders its own row; equal-revision identical state is a no-op; divergent same-Store state is a normal conflict. |
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
