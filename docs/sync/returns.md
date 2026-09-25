# Returns & Refunds Sync Contract (Phase 4A)

This document defines the `sale.return_refund.finalized.v1` transport
contract: one immutable event per finalized MoonLightRetail return/refund
transaction (`sale_corrections` row of kind `return` or `void`), durably
delivered to the Cloud inbox. Phase 4A covers transport only — no return
projection, no reporting changes. Phase 4B will project these events into
net-sales reporting.

## Business meaning

A finalized return/refund reverses part or all of one original Sale:
returned quantities restock inventory (when the line is restockable),
refund money flows back through refund payments, and the Sale status moves
to `partially_refunded`, `refunded`, or `voided`. A `void` is a full
reversal emitted as the same event shape with `kind: "void"`.

## Required fields

- `return_refund_id` (UUID): business identity of the return/refund
  transaction, distinct from transport `event_id`.
- `return_number` (`RET-YYYYMMDD-<8>` / `VOID-...`): human reference.
- `kind`: `return` | `void`.
- `reason`: one of `customer_changed_mind`, `damaged_item`, `wrong_item`,
  `cashier_mistake`, `duplicate_sale`, `wrong_products`, `wrong_payment`,
  `other`. New reasons require a new event version.
- `note`: optional, at most 500 chars.
- `sale_id` (UUID) + `sale_number`: original Sale linkage.
- `sale_event_id` (UUID, optional): original `sale.finalized.v1` event ID
  when the sale was emitted through the outbox (absent for pre-sync sales).
- `channel`: `STORE` for v1.
- `occurred_at`: finalized return business time (UTC, RFC3339).
- `shop`: the same 7-field historical receipt identity as sales.
- `actor`: handling-user snapshot (`user_id`, `user_name`); local
  operational identity only, never resolved against Cloud users.
- `currency`: `EGP` | `USD`, always the original Sale currency.
- `fx`: original Sale historical FX snapshot (`USD→EGP` required for USD,
  absent for EGP) — never a live rate.
- `totals`: `gross`, `discount`, `tax`, `refund_total` — exact sums of the
  per-line breakdowns; `refund_total` is authoritative.
- `lines[]`: `original_sale_line_id` (stable join key), `product_id` join
  hint, `quantity` > 0, `restocked`, `gross`/`discount`/`tax`/`refund`
  breakdowns, historical `cost` — where `cost` is the extended historical
  cost for the returned quantity (historical unit cost × returned
  quantity). Consumers must NOT multiply it by quantity again.
- `refunds[]`: `cash` | `card` | `other` payments summing exactly to
  `refund_total` (no tolerance); empty only for a zero refund total.

## Money representation

All money is `{"amount_minor": int64, "currency": "EGP"|"USD"}`. No floats,
no decimal strings for money. Refund payments have no `change_given`.

## Sale linkage

`sale_id` identifies the originating Sale; `sale_event_id` (when present)
points at its finalized event. Individual lines carry
`original_sale_line_id` (`sale_items.id`), which Phase 4B joins against
the original Sale projection for historical product, category, and cost
snapshots. The return event itself duplicates none of that context.

## Line linkage

Each line quantifies one returned original line: `quantity` is the returned
unit count (partial returns: less than the sold quantity), `restocked`
records whether inventory moved back. Line money preserves the Retail
discount/tax allocation for that returned quantity.

## FX lineage

For a historical USD Sale reversal, Phase 4B must reverse using the Sale's
original FX snapshot carried in this event's `fx` block — not today's FX
and not a refund-date market rate. The snapshot is byte-compared to the
original Sale snapshot in contract tests.

## Idempotency

Same rules as sales: first delivery → `accepted`; identical retry (same
event ID, same canonical payload) → `already_accepted`; same event ID with
different payload → `409 EVENT_ID_REUSE` with the original preserved.
Credential rotation and re-batching never break dedup.

## Collision semantics

- Same `event_id` + different payload → `409`, original kept.
- Different `event_id` + same `return_refund_id` → both events are
  preserved in the inbox (transport never deduplicates business identity);
  future projection arbitrates by `return_refund_id`. Retail never emits
  this shape (business IDs are UUID-generated once per transaction); it can
  only arise from a client bug or hostile input.
- Multiple distinct returns referencing one `sale_id` are all accepted:
  the business identity is the return, never the Sale.

## ACK semantics

ACK means durable `sync_events` commit. It never requires Phase 4B
projection, report visibility, or dashboard refresh.

## Retry semantics

Same taxonomy as sales: `429`/`5xx` retry with backoff; `400`/`409`/
`413`/`415`/`422` block without retry; `401`/`403` need operator
attention. Invalid return payloads never endlessly retry silently — the
desktop parks them `blocked` with a diagnostic.

## Out-of-order behavior

A return event is accepted even when the original Sale event or its
projection is not yet present. It is durably accepted in `sync_events` and
not yet projected; no return processing row exists in Phase 4A. Phase 4B
will enumerate accepted return events from `sync_events` by `event_type`
and introduce projection/processing state retroactively. Transport
validates identifier format only, never projection state.

## Payload limit

256 KiB per event, 8 MiB per body — same envelope as sales. Return events
are normally far smaller than sales.

## Phase 4B deferred behavior

Accepted return events remain unprojected: dashboard and all reports stay
finalized-sale-only (gross). Gross/net/refund reporting, category and
product refund breakdowns, cost reversal, and FX-normalized net sales are
Phase 4B. This is intentional, not a bug.
