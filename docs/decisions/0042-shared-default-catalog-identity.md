# ADR-0042: Shared Default Catalog Identity and Adoption Dependency Integrity (Phase 9-R1)

Date: 2026-10-02
Status: accepted (Phase 9-R1 remediation)

## Context

Retail migration `000001_baseline.up.sql` seeds every fresh installation
with the same fixed reference catalog UUIDs (seven categories such as
`00000000-...-0101` Islamic and five tags such as `10000000-...-0002`
Gold). Cloud 9B treated `catalog_categories.category_id` and
`catalog_tags.tag_id` as globally exclusive aggregate identities with
immutable Store ownership. Two normal fresh Retail Stores therefore
submitted the same seeded IDs under different ingress Stores; the loser
was permanently blocked with `STORE_SCOPE_CONFLICT`, products waited on
their referenced categories/tags, and Product/inventory convergence failed.
Component tests hid this by hand-crafting distinct category/tag IDs.

Independent review also proved (F02) that legacy adoption validated the
adopted row's outgoing dependencies but not its incoming dependents:
a legacy Product could adopt Store B while Store A inventory/policy
survived, and availability exposed A's stock; a legacy Category parent
could adopt B while an A child edge survived; a legacy Tag could adopt A
while attached to a B Product.

## Decision — shared default catalog identity (F04)

The fixed Retail reference catalog is a **shared, Store-less namespace**,
not a Store-owned aggregate:

- Cloud enumerates exactly the baseline seed identities in
  `internal/adapter/postgres/store_scope.go` (`sharedCategoryIDs`,
  `sharedTagIDs`). This is narrow and explicit: only those twelve IDs are
  shared; every Store-created category/tag keeps per-event Store ownership.
- A shared category/tag event is projected with `store_id = NULL`
  (Store-less) and never claims or conflicts on a Store. Shared rows may be
  referenced by any Store's products.
- A shared category may only be parented by other shared categories, so a
  Store cannot hijack the global default hierarchy with its own nodes.
- Identity comes from the fixed IDs, never from mutable labels, names, or
  graph position.
- No durable event bytes are rewritten; no Retail migration is required;
  no frozen migration is edited.

### Treatment matrix

| Concern | Behavior |
|---|---|
| Fresh installations | Both Stores' seeded IDs project once as shared rows; products converge; no `STORE_SCOPE_CONFLICT`. |
| Existing installations | Already-projected rows are unaffected; re-projection clears the Store annotation (`store_id=NULL`). Product references to a shared ID are accepted even if a legacy row still carries a Store annotation. |
| Already projected catalog rows | No backfill: rows keep data; ownership annotation is normalized to `NULL` on the next accepted revision, and shared-ID dependency checks tolerate the interim annotated state. |
| Pending durable catalog events | Unchanged. The fix is Cloud-side projection semantics; Retail outbox bytes and ordering are untouched. |
| Historical Category/Tag IDs | Sale/return snapshots remain immutable and keep the original raw IDs and labels. Historical reporting is snapshot-based and unaffected. |
| Product references | Product `top_category_id`/`subcategory_ids`/`tag_ids` referencing shared IDs are accepted regardless of the shared row's interim annotation; Store-created IDs keep strict scope checks. |
| Revisions and replay | Revision arbitration is unchanged. Equal-revision replay of identical shared state is idempotent; divergent shared state is a normal revision conflict. |
| Rebuilds | Replaying ingress events re-derives shared Store-less rows deterministically. |
| Conflicting same-ID attacks | Store-created aggregate identities remain globally arbitrated: cross-Store same-ID writes still block with `STORE_SCOPE_CONFLICT`. Shared IDs are public reference data and confer no Store authority. |

## Decision — complete adoption dependency validation (F02)

An accepted Store transition on a current-state root must not leave a
durable relationship connecting contradictory proven Store ownership:

- Product adoption validates its outgoing categories/tags **and** its
  incoming inventory, sales policy, and provider mapping ownership.
- Category adoption validates its parents, its existing child edges, and
  products referencing it as top or subcategory.
- Tag adoption validates products attached to it.
- The Product projector also takes the inventory advisory lock namespace so
  an adoption and a concurrent inventory write serialize; SERIALIZABLE
  transactions remain the backstop.
- Availability joins only policy/inventory rows that are legacy `NULL` or
  owned by the product's exact Store, and Store-aware publication
  enumerates only policies whose owning product carries the same Store.
  A pre-existing contradictory state therefore cannot expose foreign stock
  or policy as positive availability or publish it as another Store's
  resource.

Conflicting transitions fail with the existing bounded
`STORE_SCOPE_CONFLICT` code, roll back atomically, and leave the root,
dependents, and edges unchanged. The defensive read checks do not replace
the write invariant.

## Consequences

- Normal default-catalog management works across independent fresh
  installations; same-SKU products remain distinct per Store.
- Foreign inventory/policy can never become positive availability or be
  published under another Store, even for pre-fix contradictory data.
- Store-created catalog identities keep full isolation and conflict
  behavior.
- Shared default rows are read-mostly reference data: a label mutation from
  any Store updates the shared row, and historical sales keep their
  sale-time snapshots.