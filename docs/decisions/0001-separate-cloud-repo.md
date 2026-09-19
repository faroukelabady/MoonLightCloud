# ADR-0001: Separate MoonLightCloud repository

## Status

Accepted.

## Context

Desktop (MoonLightRetail: Go+Wails+Svelte, SQLite, offline-first) and cloud (always-online integration/analytics) have different runtimes, release cadences, and failure modes. One repo would tangle desktop builds with cloud deploys.

## Decision

New MoonLightCloud repo. Boundary is the versioned HTTPS contract, never shared code. No MoonLightRetail changes in Phase 1A.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
