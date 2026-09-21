# ADR-0019 — Fail-Closed Legacy Payload-Hash Policy

Date: 2026-09-21
Status: accepted (supersedes the compatibility rule of ADR-0017)
Scope: Phase 2E P2D-MED-01

## Problem

ADR-0017 allowed a v1 float64 hash fallback for pre-fix rows. That fallback
could treat materially different payload content as `already_accepted`
(stored `9007199254740993` vs incoming `9007199254740992` share the legacy
hash).

## Decision

Final rule: the v1 hash alone is never proof of payload equality. The
stored immutable payload is available, therefore exact current canonical
comparison is authoritative:

- Duplicate path re-canonicalizes the stored payload with the exact v2
  canonicalizer and compares byte-for-byte with the incoming canonical
  payload. Equal → `already_accepted`; different → `EVENT_ID_REUSE`.
- On an exact v1 match the stored `payload_hash`/`payload_hash_version`
  opportunistically upgrade to v2 in the same transaction (concurrency-safe,
  idempotent, payload never mutates).
- The v1 hash remains as historical metadata / version indicator and may
  serve as a fast negative check, never as final proof of identity.

## Consequences

Exact old retries (including float-unsafe ones and post-rotation retries)
still deduplicate; colliding variants conflict. New v2 events are
exact-only and unaffected. Covered by `TestLegacyFailClosed`,
`TestLegacySaleRetryMatrix`, `TestLegacyRotationExactRetry`.
