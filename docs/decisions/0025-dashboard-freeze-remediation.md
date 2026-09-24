# ADR-0025: Dashboard Freeze Remediation (3B-R2)

## Status

Accepted.

## Context

Two independent reviews of the Phase 3B-R dashboard found 2 residual
medium findings (daily mode vocabulary, unsafe branch bars) and 6 lows
(range UX, history, activity ordering, OpenAPI duplicates, request
vocabulary, KPI scope). All prior 3B-R security and financial fixes were
confirmed correct.

## Decision

- Daily trend takes `mode=all|EGP|USD` with response metadata
  (`mode`, `display_currency`, `normalized`); products/categories keep
  their own `all|native` vocabulary behind distinct TypeScript types
  (`DailyMode`, `BreakdownMode`, `CategoryKind`), enforced by
  `svelte-check` in the canonical gate (TypeScript pinned to 6.0.3, the
  newest svelte-check-compatible major).
- Branch bars scale with exact BigInt ratios (basis points, per-currency
  maxima); unsafe magnitudes render full bars with exact text, never zero.
- Daily unsafe magnitudes produce a chart-range state with an exact-value
  fallback list, distinct from load errors.
- Applied filters push history entries; no write occurs on initial
  load or during popstate handling. Custom ranges use draft/apply (no request until
  both dates are valid).
- Activity orders deterministically (`ts, kind, event_id`) before every
  `LIMIT`, so top-N results are stable prefixes.
- Dashboard categories send the canonical `root_category` kind per the
  documented contract and never depend on server coercion (the server
  retains its safe default for unknown kinds; that behavior is unchanged).
- Overview averages carry per-mode `units`; all Sales-card KPIs
  (transactions, units, average) follow the active currency mode.

## Consequences

No migration, no frozen-contract changes, no new runtime dependencies.
OpenAPI documents daily modes, string-money averages with units, and
deduplicated statuses; the strict YAML gate prevents duplicate-key
regressions. Revisited only by a new ADR.
