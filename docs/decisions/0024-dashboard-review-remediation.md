# ADR-0024: Dashboard Review Remediation (3B-R)

## Status

Accepted.

## Context

Independent review of Phase 3B found 4 High, 8 Medium, and 6 Low findings
against the dashboard layer only. Frozen Phase 3A reporting and Sale-sync
semantics were confirmed correct and are untouched by this ADR.

## Decision

- H01: dashboard development credentials require explicit
  `ENVIRONMENT=development` (new `envExplicit` bit); omitted environments
  fail closed even with valid explicit credentials elsewhere.
- H02: every dashboard money field serializes as a JSON decimal string;
  a lexical test walks all dashboard DTOs and fails on any numeric money
  leaf. Frontend formats with BigInt only; charts convert via a
  safe-range gate.
- H03: `/api/v1/dashboard/daily` takes `mode=all|EGP|USD` and returns
  contract metadata (`mode`, `display_currency`, `normalized`); native
  modes aggregate native buckets per Cairo date, never converted.
- H04: every normalized endpoint fails loudly on USD-without-FX
  (overview, daily, products, categories); native modes keep working.
- M01: FX normalization rounds each atomic amount with `round(numeric)`
  before aggregation, so summary, daily, and branch scopes agree.
- M02: per-mode transaction counts and truncating server averages ship in
  the overview response; the frontend renders them without arithmetic.
- M03/M04: a reusable Svelte `chart` action binds each ECharts instance to
  its live DOM node (init/dispose/resize); chart inputs validate before
  render into a stable widget error state.
- M05: `TRUSTED_PROXY_CIDRS` scopes `X-Forwarded-For` trust with a
  right-to-left algorithm; untrusted peers and malformed chains fall back
  to the direct peer.
- M06: browser sync-health carries allowlisted codes plus mapped bilingual
  labels; raw stored messages stay in logs/tables/admin tooling.
- M07: `queue_count` (pending + retry exactly once) is the displayed
  queue; pending/retry remain as a sub-breakdown.
- M08/L02: subcategory views are bar-only with the facet explanation; all
  inline style attributes were removed in favor of CSS classes, keeping the
  strict CSP violation-free.
- L01/L03/L04/L05/L06: URL-synced custom inputs (typing-preserving),
  `now >= exp` session expiry, 404 for missing `/dashboard/assets/*`,
  truthful "Latest Sale event received" wording, timestamp+kind+event_id
  activity ordering. 503 renders a dedicated Retry action that re-fetches.

## Consequences

No migration, no frozen-contract changes, no new dependencies. OpenAPI
documents the split row shapes, daily modes, string money, queue field,
and 500/503 responses. Revisited only by a new ADR.
