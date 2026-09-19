# ADR-0004: Podman/Docker-compatible OCI development

## Status

Accepted.

## Context

Team runs Podman; CI/hosts speak Docker. Dev must mirror production containers without Host-specific tooling.

## Decision

deploy/compose.yaml works with podman compose and docker compose. Multi-stage Containerfile, non-root, stateless.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
