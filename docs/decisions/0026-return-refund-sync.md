# ADR-0026 — Returns & Refunds Sync Contract

Date: 2026-09-25
Status: accepted
Scope: Phase 4A

## Context

Finalized returns/refunds exist authoritatively in MoonLightRetail
(`sale_corrections` of kind `return`/`void`) but never reach MoonLightCloud.
Phase 4B needs them for net-sales reporting without consulting mutable
catalog state.

## Decision

1. One immutable event per finalized return/refund transaction:
   `sale.return_refund.finalized.v1`, reusing the frozen sale-sync envelope,
   outbox, transport, auth, canonicalization, idempotency, and ACK semantics.
2. Original Sale lineage is authoritative: the event links `sale_id`,
   `sale_number`, `sale_event_id` (when the sale was emitted), and
   `original_sale_line_id` per line. Historical product/category/cost
   context stays in the original Sale projection; Phase 4B joins on these
   stable keys.
3. Transport accepts before projection dependency resolution: a return is
   durably accepted even if the original Sale event/projection is absent.
   Out-of-order arrival is normal (offline retail, retries); cumulative
   validation (over-return) is Phase 4B projection, never transport.
4. The original historical FX snapshot (`USD→EGP`, exact microrate) is
   carried verbatim so Phase 4B reverses historical USD sales at the sale
   rate, never a live rate.
5. Projection and reporting are deferred to Phase 4B: accepted returns stay
   pending, dashboards and reports remain finalized-sale-only (gross).

## Alternatives rejected

- Separate returns outbox/daemon/endpoint: duplicates frozen, reviewed
  infrastructure for no benefit.
- One-return-per-sale ownership keyed by `sale_id`: wrong identity —
  multiple valid returns may reference one sale; business identity is the
  return transaction (`return_refund_id`).
- Cumulative over-return rejection at ingestion: couples transport to
  projection state and breaks out-of-order acceptance.
- Duplicating full product/category snapshots into the return event:
  contradicts the single-authority rule and risks divergent history.

## Consequences

Phase 4B can implement gross/refund/net sales, returned units, product and
category refund breakdowns, branch reporting, cost reversal, and FX
reversal purely from inbox events plus the original Sale projection.
