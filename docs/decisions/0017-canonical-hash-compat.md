# ADR-0017 — Exact Canonical JSON and Payload-Hash Compatibility

Date: 2026-09-21
Status: accepted
Scope: Phase 2D HIGH-03 (+ §60-61 hash compatibility)

## Context

The pre-2D canonicalizer decoded payloads into `map[string]any` without
`UseNumber`, so every JSON number passed through `float64`. Integers above
2^53 lost precision (`9007199254740993` hashed as `9007199254740992`), so
distinct payloads could share a `payload_hash` and idempotency comparisons
were unsound for large integers.

## Decision

1. New canonicalizer (`internal/sync/canonical.go`): `json.Decoder.UseNumber()`
   plus bounded exact decimal normalization. Numeric rule A: `1`, `1.0`,
   `1e0`, `10e-1` canonicalize identically. No float64 anywhere on the path;
   hostile exponents that would force >10,000 chars are rejected as malformed
   before ACK. `sale.finalized.v1` money stays strictly integer at DTO level.
2. `sync_events.payload_hash_version` (migration 00006): pre-fix rows are v1,
   all new rows are v2 (exact). No hash backfill — stored v1 hashes stay.
3. Duplicate-path comparison: exact hash first; for v1 rows only, fall back
   to the incoming legacy hash when the incoming payload itself is
   float-unsafe (exact ≠ legacy bytes), i.e. a legitimate retry of a
   pre-fix float-affected payload. Float-safe incoming payloads with a
   different exact hash always conflict, even on v1 rows.
4. New events never use the legacy algorithm for storage or comparison.

## Consequences

- Legitimate pre-fix events (including float-affected ones) still
  deduplicate after upgrade: identical retry → `already_accepted`.
- The old float weakness is not perpetuated: v2 rows are exact-only, and
  the v1 residual is bounded to dedup-masking on legacy rows (no new state;
  projection always reads the immutable stored row). The one inherently
  ambiguous case — a v1 row whose stored bytes were already rounded, matched
  by the rounded variant — is accepted as `already_accepted` with zero new
  rows; it cannot create or alter a projection.
- Covered by `TestCanonical*` (unit) and `TestLegacyHashCompatibility`
  (PostgreSQL migration/integration).
