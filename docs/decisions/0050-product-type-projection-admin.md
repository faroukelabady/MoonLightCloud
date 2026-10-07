# ADR-0050: Product Type Projection & Admin Control (Phase 17-R2)

- Status: Accepted (Phase 17-R2, pre-freeze extension of ADR-0049).
- Extends: ADR-0049 (Product & Physical Variant Architecture, Cloud side).
  Retail ADR-039 is the authority for domain semantics; this ADR records
  only Cloud projection/control consequences.
- Scope: Store-scoped projections, revision-gated sync, durable admin
  commands, dashboard UI. No business-state mutation outside Retail.

## Context

Retail now owns structural ProductTypes (allowed dimensions +
capabilities, `frame_configuration` only). Cloud must project them,
attach them to product projections, freeze them on sale lines, and offer
durable admin control without ever becoming authority.

## Decision

- **Schema:** `00037_product_types` (`catalog_product_types` + dimensions +
  capabilities tables, `catalog_products.product_type_id` logical ref, no
  destructive FK) and `00038_sale_line_product_type_snapshot` (4 nullable
  sale-line columns, event-sourced only).
- **Ingestion:** `catalog.product_type.snapshot.v1` validated
  (UUID/code/names/position/revision/bounded sets/known capabilities
  only); `catalog.product.snapshot.v2` requires `product_type_id`;
  `sale.finalized.v3` carries optional all-or-nothing type snapshot
  (id+code+both labels, UUID/code/label bounds).
- **Projection:** independent `catalog_product_type_projection.v1` worker
  (type_revision gate, Store scope gate, upsert + set replace, equal-revision
  conflict compare, supersede handling). Product projector writes
  `product_type_id` (v1 legacy NULL tolerated; v2 required). Type-less
  products project with NULL (never fake types, never dropped relations —
  dependency wait where the graph requires it).
- **Admin commands:** 6 typed commands
  (`catalog.product-type.create/details/dimensions/capabilities/status.v1`,
  `catalog.product.type.assign.v1`) with Store binding, expected revisions
  (type stream vs product stream for assign), canonical payload hash,
  idempotency, restart safety. Creation mints identity Retail-side (Cloud
  validates shape only; unknown entity by design). Ownership via
  `catalog_product_types.type_revision` (type mutations) and
  `catalog_products.source_revision` (assign).
- **Admin reads/UX:** Store-scoped `AdminProductTypeList/Detail` (+ dims/caps),
  `product_type_id` on product rows/detail, `GET product-types` + `/{id}`
  endpoints, dashboard Types tab with PENDING/DELIVERED/APPLIED/CONVERGED/
  CONFLICT/REJECTED truthfulness (no optimistic claims).
- **Commerce:** provider-neutral; zero type-name branching; frames only via
  capability (verified by absence of `product_type` logic in provider code).
- **Reports/Health:** type-aware groupings without join multiplication;
  health codes for missing/dangling/inactive type states (informational vs
  defect distinguished).

## Consequences

- `TargetVersion` 40 (39: command vocabulary; 40: create identity). sqlc regenerated. OpenAPI strict+semantic updated.
- Projectors run in `main.go` alongside variant workers.
- Dashboard types management + products-by-type filtering.

## R3 create-command identity closure (pre-freeze)

A ProductType creation request must not pretend a placeholder UUID is the
business identity. Final semantics:

- The command carries NO business entity: `entity_id` is the explicit
  absent marker `""`, `target_kind = 'create'`, and the requested stable
  key travels as `requested_key` (code). No field named `entity_id` may
  contain a knowingly fake business identity (00040 coherence CHECK).
- Retail owns identity: `ProductTypeService` mints the canonical UUID at
  apply; the apply Result AND the durable receipt persist the REAL ID
  (duplicate delivery replays it — the poller can no longer clobber the
  Cloud target with `""`).
- Cloud persists the Retail-minted ID in `result_entity_id` when a target
  reports APPLIED (first writer wins; intent never rewritten) and
  evaluates convergence on it (APPLIED without a result cannot converge).
  Admin history shows requested intent + result identity distinctly.
- Same code from an unrelated command conflicts truthfully (no adoption);
  the code-uniqueness fence is the cross-device last defense.
- Multi-device: creation is single-flight per intent — one authoritative
  target (lowest device UUID, deterministic). Update commands keep
  multi-device fan-out (idempotent). If the chosen device never polls,
  the command stays PENDING (cancellable while pending).

## Non-goals

Same as Retail ADR-039 §92. No Cloud-only attribute definitions; Cloud
projects/displays Retail definitions, never invents them.
