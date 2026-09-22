# ADR-0021: Sales Reporting Audit Remediation (3A-R)

## Status

Accepted.

## Context

Independent audit of Phase 3A reporting found six correctness issues: UTC
instant-based relative periods missed Cairo civil dates across DST (H1);
omitted environment plus omitted token exposed reports (H2); cashier and
channel rows reused `line_sales_minor` for header revenue (H3); line cost
ignored quantity (H4); category renames collapsed by ID (M1); nested
currency buckets relied on SQL order (L1).

## Decision

- H1: relative periods move on an explicit `CivilDate` (extracted from the
  request instant in store time) with gap-safe midnight resolution kept.
- H2: fail-closed reporting auth — token mode anywhere, else open only
  with explicit development environment plus explicit opt-in flag; missing
  environment never grants access.
- H3: mutually exclusive row groups — line dimensions carry `line_sales`
  (one stable pre-adjustment meaning); cashier/channel carry header
  `currency_totals` (subtotal/discount/tax/sales_total) and no `line_sales`.
- H4: extended cost = unit `cost_minor` × quantity, exact numeric math with
  explicit `bigint` casts (per-line and aggregate overflow fail loudly).
- M1: category grouping by snapshot tuple (kind + ID + both names).
- L1: all nested currency arrays sorted alphabetically in the service.

## Consequences

No architecture, sync, migration, or feature changes. OpenAPI, ops guide,
and README updated to the exact runtime contract. Revisited only by a new
ADR.
