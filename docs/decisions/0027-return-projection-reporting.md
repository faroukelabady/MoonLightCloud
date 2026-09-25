# ADR-0027 — Returns/Refunds Projection & Reporting Integration

Date: 2026-09-25
Status: accepted
Scope: Phase 4B

## Context

Phase 4A froze `sale.return_refund.finalized.v1` transport: validated
before ACK, durably stored in `sync_events`, never projected. Reports and
dashboards remain finalized-sale-only (gross). Phase 4B must turn those
inbox events into money-accurate net reporting without touching frozen
Sale economics, identity, or transport.

## Decision

1. **Additive immutable return projection.** New tables
   `return_refund_projection` (+ lines, + refund payments) keyed by
   business `return_refund_id`, plus `return_refund_ownership`
   (`return_refund_id` PK, `winning_event_id` UNIQUE) reusing the frozen
   Sale arbitration principle keyed by business identity — never by
   `sale_id`, since many returns per sale are valid. Sale projection rows
   are never mutated; net is derived by report queries.
2. **Dedicated processor, shared mechanism.**
   `return_refund_projection.v1` reuses `sync_event_processing`
   (pending/retry/blocked/processed), claim/lock/mark, backoff, CLI, and
   rebuild discipline. Phase 4A rows have no processing rows, so backfill
   discovery (missing row = discoverable) projects history with no resend.
3. **Dependency wait, not rejection.** A return whose sale projection (or
   sale line) is absent retries with `SALE_DEPENDENCY_WAIT`; it projects
   once the sale appears. A sale projection that exists but lacks the
   referenced line, a currency/FX mismatch, or a cumulative breach that
   committed history makes impossible is terminally blocked with an
   explicit code. Returns serialize per sale on a parent-row lock so
   cumulative guards (returned qty <= sold qty; cumulative refunds <= sale
   total, both telescoping-exact under the frozen allocation) cannot
   write-skew.
4. **Historical attribution.** Product/category/cost context comes from
   the original Sale projection via `original_sale_line_id`; FX reversal
   uses the event's historical snapshot with agreement checked against
   the sale snapshot; shop/actor snapshots come from the event. Extended
   return-line cost is consumed directly, never re-multiplied. Reports
   use return `occurred_at` (Cairo civil dates, half-open UTC) — never
   the sale date, never `received_at`.
5. **Additive reporting, signed net.** New `refund_total_minor`,
   `net_sales_minor`, `returned_units`, `returned_cost_minor`,
   `net_cost_minor` ride alongside frozen gross fields;
   `sales_total_minor` keeps meaning finalized-sale gross;
   `transaction_count` stays sale-only with a separate return count.
   Net may be negative (never clamped); net DTOs use signed integers,
   never the nonnegative intake `Money` schema. Native EGP/USD stay
   separate; All-mode normalizes each atomic event with its own
   historical FX (round-once-then-sum, same as sales).
6. **Cashier semantics.** Cashier-dimension refunds attribute to the
   original sale's cashier (net performance); return-actor identity
   appears in activity/problem surfaces, not subtracted from another
   cashier's sales. Documented, not guessed.
7. **Dashboard truthfulness.** Net Sales becomes the primary KPI with
   Gross and Refunds visible; units/transactions split sale vs return;
   Sync Health shows sale and return completeness separately and drops
   the Phase 4A exclusion note.

## Alternatives rejected

- Mutating `sales_projection` in place: destroys auditability and
  rebuildability.
- Cumulative validation at ingestion: couples transport to projection
  state, breaks out-of-order acceptance (frozen 4A contract).
- One-return-per-sale ownership: contradicts valid multi-return business.
- Clamping negative net to zero: hides real refund-heavy periods.
- Reusing nonnegative `Money` schema for net fields: forbids legal
  negatives.

## Consequences

Reports expose gross/refund/net, units sold/returned, cost
gross/returned/net across summary/daily/product/root/subcategory/
cashier/channel/branch scopes with Cairo date semantics preserved;
dashboard renders them with string-exact money; rebuilds reproduce
winners and totals byte-identically from `sync_events`.
