# ADR-0013: Device credential lifecycle (separate credentials, rotation)

## Status

Accepted.

## Context

Phase-1A single-verifier-per-device cannot rotate without lockout windows and conflates device state with secret state.

## Decision

devices + device_credentials tables; token <device-id>.<credential-id>.<secret>; rotation is one transaction (insert successor, revoke predecessors under a device-row lock so concurrent rotations leave exactly one active); device revoke kills all credentials; credential revoke kills one. last_seen_at per request on device, last_used_at per credential.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
