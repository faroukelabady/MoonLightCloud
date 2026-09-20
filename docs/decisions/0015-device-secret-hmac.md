# ADR-0015: Final device-secret construction: HMAC-SHA256, base64 pepper

## Status

Accepted.

## Context

Phase-1A left SHA-256-vs-HMAC ambiguous and allowed empty peppers. High-entropy 256-bit secrets need a keyed verifier, unambiguous config, and a migration path for 1A rows.

## Decision

verifier = HMAC-SHA256(key=pepper[32B], message=salt[16B] || hex_secret); pepper is base64 StdEncoding decoding to exactly 32 bytes (dev default documented, rejected outside dev); legacy 1A rows migrate as verifier_version=0 verified with the exact old construction; pepper_version column enables future rotation; changing pepper invalidates v1 verifiers (documented, no silent surprise). Supersedes ADR-0011.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
