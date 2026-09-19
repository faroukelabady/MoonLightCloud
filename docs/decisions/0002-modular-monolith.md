# ADR-0002: Modular monolith over microservices

## Status

Accepted.

## Context

Initial load is near zero; a distributed system would add ops cost without benefit. Module boundaries (auth/sync/commerce/notifications + adapters) keep future extraction possible.

## Decision

Single Go binary, clean internal packages, ports/adapters. No Kafka/Redis/K8s until measured need.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
