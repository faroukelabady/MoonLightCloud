# ADR-0033 — Online Order Ingestion (Phase 6C)

Date: 2026-09-27
Status: accepted
Scope: Phase 6C (Cloud-only; Retail untouched, no Retail Sale fabrication)

## Context

Phase 6B publishes MoonLight state outward. The reverse direction —
learning about online orders — needs a provider-neutral inbound
boundary that cannot corrupt catalog, inventory, or sales authority,
leak customer PII, or confuse operator reporting.

## R1 remediation (order concurrency, lease fencing, lifecycle)

The 6C freeze review demonstrated three ordering races plus two
follow-ups, closed without redesigning the domain:

1. **Reconciliation generations.** Every current-state reconcile
   (webhook created/updated, delete confirmation, manual sync-order)
   first obtains a per-order generation token
   (`commerce_online_order_reconcile_fences`), then GETs provider
   state, then projects under a fenced check inside the projection
   transaction (fence row locked first, order rows second). A token
   older than the fence is superseded and mutates nothing. The later
   starter wins because it initiates the later current-state read;
   MoonLight cannot fix stale reads inside Woo itself. No DB lock ever
   spans provider HTTP. Generation is operational metadata, never the
   semantic order revision and never publicly exposed.
2. **Lease generations.** Every webhook claim increments
   `lease_generation`; every finish requires owner + generation on a
   non-terminal row and reports applied vs stale no-op. Expired
   workers can neither resurrect terminal states nor touch
   scheduling/error metadata owned by newer leases.
3. **Delete requires current confirmation.** `order.deleted` triggers
   GET-then-decide under a generation: live provider state wins (no
   tombstone), confirmed 404 tombstones, transient failures retry,
   terminal provider errors block. A stale delete generation cannot
   commit after a newer live reconcile.
4. **Delivery metadata conflicts** (same delivery + hash but different
   topic or order ID) are 409 like payload conflicts.
5. **Sales isolation** is now DB-proven: Woo order lifecycles never
   move frozen Sale/Return metrics.

## R2 remediation (order error semantics, read-model closure)

The R1 freeze review left one HIGH plus three smaller findings,
closed without touching fencing, revisions, or the projection schema:

1. **Exact 404/409 order semantics.** The shared Woo client classifies
   both HTTP 404 and 409 as generic Conflict (frozen 6B behavior,
   unchanged). The order reader previously mapped any Conflict kind to
   ORDER_NOT_FOUND, so a 409 behind `order.deleted` could tombstone a
   live order. `GetOrder` now uses the exact HTTP status the client
   already returns: only 404 becomes ORDER_NOT_FOUND; 409 stays
   Conflict and blocks (delete, created/updated, and manual
   sync-order alike) without mutating the projection.
2. **Orders cursor pagination.** The store already keyset-paginated
   (`created_at DESC, provider_key, external_order_id`) but HTTP/UI
   exposed only the first page. `GET /api/v1/dashboard/orders` now
   accepts an opaque versioned cursor and returns `next_cursor`
   (lookahead row, never a count), with filter-bound tokens, 400 on
   misuse, and a UI Load-more continuation that resets on filter
   change.
3. **Oldest-pending metric.** The webhook inbox stat used
   `max(received_at)` (newest) for a value named oldest; it now uses
   `min(received_at)` over pending/retry.
4. **Secret separation enforced.** The documented webhook/REST secret
   distinction is now startup validation: identical
   `COMMERCE_WOO_WEBHOOK_SECRET` and `COMMERCE_WOO_CONSUMER_SECRET`
   fail config with value-free errors; disabled mode is unchanged.

## Decision

1. **Webhooks are triggers, not versions.** Signed Woo
   `order.created/updated/deleted` deliveries persist minimal
   metadata + payload hash (never raw bodies) and wake a worker; each
   attempt re-reads provider current state via `GET /orders/{id}`, so
   reordered/duplicate webhooks converge instead of regressing.
2. **Separate order capability.** The frozen `CommerceProvider`
   interface is untouched; `CommerceOrderProvider.GetOrder` is
   asserted per call with a typed capability error. Woo implements
   both over the frozen hardened HTTP client.
3. **Current-state projection.** `(provider_key, external_order_id)`
   rows carry a monotonic MoonLight revision advanced only by
   semantic-fingerprint change, with atomic header/lines/addresses
   replacement and append-only status history on status transitions.
   Unknown provider statuses ingest as UNKNOWN; deletions tombstone
   with last-known data preserved.
4. **Exact money, UTC time.** Decimal strings parse to int64 minor
   units without floats (EGP/USD only); GMT timestamps persist as
   UTC; dashboard renders string money end to end.
5. **Mapping without fabrication.** Lines resolve through the frozen
   generic product mapping (variations never resolve); unmapped lines
   persist with snapshots and completeness flags. Unknown lines never
   create products, mappings, or catalog edits.
6. **Durable worker.** Pending/retry/processed/blocked inbox with
   `FOR UPDATE SKIP LOCKED` claims, bounded leases (no forever state),
   release-before-I/O, classified retries honoring `Retry-After`,
   machine-readable error codes only.
7. **PII minimization.** No raw webhook bodies, IPs, user-agents, or
   unnecessary metadata persisted; customer contact stays out of
   logs, errors, metrics, and URLs; dashboard order APIs need the
   operator session while webhooks need only HMAC.
8. **Hard boundaries.** No Sale fabrication or reporting contact, no
   inventory mutation or reservation, no automatic `SetInventory` on
   order receipt, no Woo order writes, no payments/refunds/
   fulfillment, no dashboard actions, no schedulers or webhooks
   beyond the three order topics.

## Consequences

- 7A/7B can consume order facts for notifications/reports without
  Woo-specific code; 7C can later bridge Cloud orders to Retail
  awareness over an explicit contract.
- `sync-order` repairs current state from the provider at any time;
  raw bodies were never kept, so replay means re-fetch, not re-parse.
