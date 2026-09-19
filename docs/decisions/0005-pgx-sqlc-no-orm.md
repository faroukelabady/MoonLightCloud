# ADR-0005: pgx + sqlc, no ORM

## Status

Accepted.

## Context

SQL must stay visible/reviewable for financial data. ORM magic hides queries and migrations.

## Decision

pgx v5 + sqlc generated code (never hand-edited) + goose migrations. Domain packages never import pgx.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
