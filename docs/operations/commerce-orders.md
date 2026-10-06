# Online Order Ingestion Operations (Phase 6C)

Inbound WooCommerce orders → provider-neutral MoonLight projections.
Read-only with respect to catalog, inventory, and sales: ingestion
never fabricates Sales, mutates stock, reserves inventory, or writes
back to Woo.

## Enablement

| Variable | Required | Rule |
|---|---|---|
| `COMMERCE_WOO_ENABLED` | yes | The Woo provider itself must be on |
| `COMMERCE_WOO_ORDERS_ENABLED` | yes | `true` enables webhook route + processor |
| `COMMERCE_WOO_WEBHOOK_SECRET` | when enabled | ≥ 32 chars, enforced distinct from the REST consumer secret, runtime only |

Disabled (default): webhook route unregistered (404), no processor
runs, no secret required. Enabling never requires Woo network access
at startup; connectivity is needed only when reconciling.

## Woo webhook creation (manual)

In WooCommerce → Settings → Advanced → Webhooks, add three webhooks:

| Topic | Delivery URL |
|---|---|
| `order.created` | `https://<cloud>/api/v1/commerce/webhooks/woocommerce/<provider_key>` |
| `order.updated` | same URL |
| `order.deleted` | same URL |

Set each webhook's secret to exactly `COMMERCE_WOO_WEBHOOK_SECRET`
(the value Woo uses for the `X-WC-Webhook-Signature` HMAC). No webhook
is ever created automatically by Cloud. Existing Product/Order Read
API credentials are sufficient; no extra permissions are needed.

## Delivery URL

`POST /api/v1/commerce/webhooks/woocommerce/{provider_key}` with Woo
headers (`X-WC-Webhook-Signature`, `X-WC-Webhook-Topic`,
`X-WC-Webhook-Resource: order`, `X-WC-Webhook-Delivery-ID`).
Responses: `202` accepted (or idempotent duplicate), `400` bad
topic/resource/body/identity, `401` bad signature (nothing persisted),
`409` delivery-ID reuse with a different body, `413` over 1 MiB.

## Concurrency model

Every current-state reconcile — webhook created/updated, delete
confirmation, manual `sync-order` — first takes a per-order
reconciliation generation, then reads provider state, then verifies
the token inside the projection transaction (fence row locked before
order rows). A superseded token mutates nothing: no header, lines,
history, or revision change. Later-started reconciliations win
because they initiate later current-state reads. Generations are
internal operational state, never order revisions and never public.

Webhook claims carry lease generations: each claim increments, and
every finish requires the claiming owner + generation on a
non-terminal row. Stale completions are safe no-ops that change
nothing — terminal states cannot resurrect.

Deletion never trusts the topic alone: each attempt confirms current
provider state (live wins, transient retries). Only an actual HTTP 404
from Woo becomes ORDER_NOT_FOUND and tombstones; HTTP 409 stays a
generic Conflict and blocks without tombstoning. No DB
lock or transaction ever spans provider HTTP in any path.

## Processing states

`pending → retry → processed`, or `blocked` with a machine-readable
`last_error_code` (`ORDER_NOT_FOUND`, `ORDER_CURRENCY_UNSUPPORTED`,
`ORDER_INVALID`, `ORDER_CONFLICT`, `ORDER_PROVIDER_*`). Temporary and
rate-limited provider failures retry with bounded backoff honoring
`Retry-After`; crashed claims become eligible after the 5-minute lease.
No permanent `processing` state exists.

## Blocked/retry diagnosis

```sql
-- Blocked events with reasons.
SELECT provider_key, delivery_id, topic, external_order_id, attempt_count, last_error_code
FROM commerce_online_order_webhook_events WHERE status = 'blocked' ORDER BY received_at DESC LIMIT 20;
-- Retry backlog and oldest age.
SELECT status, count(*), min(received_at) FROM commerce_online_order_webhook_events
WHERE status IN ('pending', 'retry') GROUP BY status;
-- Current order behind an event.
SELECT canonical_status, revision, mapping_complete, unmapped_lines, provider_deleted
FROM commerce_online_orders WHERE provider_key = $1 AND external_order_id = $2;
```

Recover via `moonlight-cloud commerce sync-order --provider <key> --order <id>`
(current provider state reconciled directly) or, for genuinely fixed
remote state, the next webhook redelivery. There is no bulk resync.

## Mapping incomplete meaning

`mapping_complete=false` with `unmapped_line_count=N` means N lines
reference Woo products with no `commerce_product_mappings` row (manual
Woo products or variations). The order is valid and visible; map the
product in Woo-managed flow and re-run `sync-order` to resolve it.
Nothing is auto-created.

Since Phase 17 a line also carries its immutable variant snapshot
(`variant_id`, `variant_sku`, `variant_attribute_snapshot`) captured at
ingestion; an order with an unresolved provider selection is never
reported fully mapped. See `docs/operations/product-variants.md`.

## PII handling

Webhook bodies are never persisted — only delivery metadata and the
SHA-256 body hash. Customer contact lives in the order projection for
fulfillment visibility (operator session required) and never in logs,
errors, metrics, or URLs. Logs carry provider/order/status/revision
only.

## Provider outage behavior

Valid webhooks still return 202 while Woo is down; events wait in
`retry` with backoff and converge when Woo recovers. Order data stays
at last-known state meanwhile.

## Deleted order behavior

`order.deleted` tombstones only on provider-confirmed absence (HTTP
404 → ORDER_NOT_FOUND) with last-known lines preserved
(`provider_deleted=true`, canonical `DELETED`); unseen orders get a
minimal tombstone without fabricated data. A live provider order
(including an HTTP 409 Conflict response) never tombstones: the event
blocks and the live projection stands. History is retained for
audit. Repeat deletes are idempotent.

## Order list pagination

`GET /api/v1/dashboard/orders` pages by keyset (`created_at DESC,
provider_key, external_order_id`): `limit` defaults to 20 (max 100),
`cursor` is an opaque versioned token from the previous
`next_cursor` (`null` on the final page). Cursors carry only list
position plus the filter set they were issued for — no PII or
secrets — and are rejected (400) when malformed, oversized, or reused
under different `provider`/`status` filters. The UI appends pages via
Load more and resets pagination whenever filters change.

## Backup significance

`commerce_online_orders`, lines, addresses, status history, the webhook
inbox, and the reconciliation fence table are durable integration
state (like provider mappings): back them up; no catalog/policy/
inventory rebuild touches them, and raw bodies were never kept so
replay means re-fetch via `sync-order`. Fences are operational (not
business history) but must survive restarts and instances; never
garbage-collect them automatically in R1.
