# Sales Reporting Operations

Reports read the frozen `sale.finalized.v1` projection. Reporting is
read-only: zero business-state side effects per request.

## Configuration

- `STORE_TIMEZONE`: IANA identifier (production: `Africa/Cairo`, explicit
  and validated at startup; invalid values fail boot). Development defaults
  to `Africa/Cairo`.
- `REPORTING_API_TOKEN`: temporary report-read Bearer secret (high-entropy,
  16+ chars). Required in staging/production; development may leave it empty
  (open reports + one startup warning). Never logged. Report scope only —
  device sync credentials are rejected on report routes. Replace with
  dashboard user auth when it lands (this token is documented temporary).

## Example calls

```bash
AUTH="Bearer $REPORTING_API_TOKEN"
base=http://localhost:8080/api/v1/reports/sales

curl -H "Authorization: $AUTH" "$base/summary?period=today"
curl -H "Authorization: $AUTH" "$base/summary?period=last_10_completed_days"
curl -H "Authorization: $AUTH" "$base/summary?period=custom&from_date=2026-09-01&to_date=2026-09-15"
curl -H "Authorization: $AUTH" "$base/daily?period=last_10_completed_days"
curl -H "Authorization: $AUTH" "$base/breakdown?period=today&dimension=product"
curl -H "Authorization: $AUTH" "$base/breakdown?period=today&dimension=subcategory"
```

## Period semantics

- `today`: store-local midnight through the request instant (incomplete day).
- `yesterday`: prior complete store-local day.
- `last_10_completed_days`: 10 completed store-local dates ending at today
  midnight; excludes the partial current day; exact across DST.
- `custom`: inclusive `from_date`/`to_date` (YYYY-MM-DD) in store time,
  queried as half-open `[from midnight, day-after-to midnight)`. Max 366
  calendar days.

## Reading freshness

`freshness` in every response:

- `latest_sale_event_received_at`: newest accepted sale event (transport).
- `latest_projected_sale_occurred_at`: newest projected business time.
- `projection_backlog_count`: accepted events without terminal
  processed/blocked state (includes not-yet-discovered work).
- `blocked_sale_event_count`: terminal integrity conflicts (e.g.
  SALE_ID_CONFLICT) — excluded from totals by design.
- `cloud_projection_complete`: true when backlog is zero. This means
  Cloud has nothing left to project — it does NOT mean Retail has no
  unsent outbox events. Cloud cannot observe the Desktop outbox.

Terminology is gross finalized sales: refunds are not yet synchronized
(Phase 4B introduces net reporting). Subcategory rows are facet-style and
may sum above line totals; root-category rows are additive. Product
`line_sales_minor` excludes Sale-level discount/tax (no allocation).
