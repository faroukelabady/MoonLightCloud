# ADR-0022: Final Breakdown DTO Split (Line vs Header Shapes)

## Status

Accepted.

## Context

Audit remediation 3A-R split breakdown finances into exclusive row groups
but kept one shared `CurrencyTotal` element type, which forced a false
`line_cost_minor: 0` onto cashier/channel rows whenever cost was known and
nonzero. A zero must mean zero cost, never "not calculated".

## Decision

- Line dimensions (`product`, `root_category`, `subcategory`) use
  `LineCurrencyTotal`-shaped buckets: `currency`, `line_sales_minor`,
  `line_cost_minor` (extended unit cost × quantity).
- Header dimensions (`cashier`, `channel`) use `SaleCurrencyTotal` buckets:
  `currency`, `subtotal_minor`, `discount_minor`, `tax_minor`,
  `sales_total_minor` — no cost field at all.
- OpenAPI expresses the split structurally (`LineBreakdownRow` /
  `HeaderBreakdownRow` under `oneOf`), not by prose alone.
- Category ordering tie-breaks on the full snapshot identity
  (kind, ID, both names) after units, so equal-unit renames order
  deterministically without SQL arrival-order dependence.

## Consequences

Runtime JSON, OpenAPI, ops guide, and tests agree field-by-field;
summary/daily cost semantics unchanged. Revisited only by a new ADR.
