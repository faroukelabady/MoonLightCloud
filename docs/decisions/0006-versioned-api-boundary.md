# ADR-0006: Versioned API boundary with MoonLightRetail

## Status

Accepted.

## Context

Desktop and cloud ship independently; unversioned APIs would couple releases and break offline desktops.

## Decision

All business APIs under /api/v1. Health/version outside. Backward compatibility is a release requirement from Phase 1B on.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
