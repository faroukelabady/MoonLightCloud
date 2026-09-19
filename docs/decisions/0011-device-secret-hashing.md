# ADR-0011: Device secret hashing design

## Status

Accepted.

## Context

Device secrets are 256-bit random tokens, not human passwords: slow KDFs (bcrypt/argon2) add latency per API call with no security gain at 256-bit entropy.

## Decision

Salted keyed hash: per-device 128-bit salt, SHA-256(salt||secret), HMAC-SHA256 when DEVICE_SECRET_PEPPER set (required in prod). Constant-time compare. Raw shown once, never stored/logged. See docs/security/threat-model.md.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
