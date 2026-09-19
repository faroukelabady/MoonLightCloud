# ADR-0009: No shared desktop/cloud domain package

## Status

Accepted.

## Context

Shared libraries couple release trains and blur ownership; duplication across a versioned contract is cheaper and safer.

## Decision

No MoonLightShared/Common. Contract-first: api/openapi.yaml. Intentional model duplication.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
