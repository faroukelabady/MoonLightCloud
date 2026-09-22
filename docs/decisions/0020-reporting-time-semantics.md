# ADR-0020: Sales Reporting and Store-Timezone Semantics

## Status

Accepted.

## Context

Phase 3B dashboards and Phase 7B scheduled WhatsApp reports need one
reporting authority over historical finalized Sales. Business days are
store-local (Africa/Cairo, which observes DST), while storage stays UTC.
Mixed currencies must never merge; subcategory facets are non-additive.

## Decision

- Business timestamp = `sales_projection.occurred_at`; transport and
  processing timestamps never decide report days.
- Business timezone = `STORE_TIMEZONE` (IANA, validated at startup,
  Africa/Cairo default in dev, explicit in staging/production, tzdata
  embedded in the binary for distroless).
- Periods resolve local calendar boundaries with `AddDate` (never 24h
  math) into half-open UTC windows `[start, end)`; `BETWEEN` is banned
  for time filtering.
- Money stays integer minor units in per-currency buckets (EGP|USD);
  aggregate overflow fails explicitly via `numeric→bigint` casts.
- Reporting calculations live in `ReportingService` (handler → service →
  repository → PostgreSQL); breakdown dimensions are a fixed enum over
  static sqlc queries.
- Report auth is the temporary `REPORTING_API_TOKEN` Bearer guard
  (report-read scope only) until dashboard user auth exists; device sync
  credentials are never valid for reports.
- Freshness metadata reports Cloud projection state only and never claims
  Desktop sync completeness.

## Consequences

One authority serves dashboards and jobs; DST-safe day math is unit-tested
against real IANA transitions; subcategory non-additivity and
no-refund-yet terminology are contractual. Revisited only by a new ADR.
