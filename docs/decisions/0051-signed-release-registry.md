# ADR-0051: Signed Release Registry (Phase 18)

- Status: Accepted (Phase 18).
- Authority: MoonLightRetail ADR-040 defines the release envelope and the
  Retail trust root. This ADR records only Cloud's registry role.
- Scope: importing, storing and revoking signed Retail releases. Cloud
  never builds, signs or alters a release.

## Context

Retail self-update (Phase 18) needs a place to publish which signed
releases exist and where their artifacts live. Cloud is internet-facing.
If Cloud could mint or modify releases, a Cloud compromise would become
remote code execution on every shop PC.

## Decision

- **Import only signed envelopes.** `POST /api/v1/dashboard/releases`
  accepts:
  - the `moonlight-release-envelope-v1` document exactly as produced by
    offline release tooling;
  - one HTTPS location per signed artifact file name.
- **No typed identity.** Version, commit, sequence, hashes, sizes and
  signature come only from the verified manifest. A body with invented
  identity fields is rejected (strict decoding).
- **Verification mirrors Retail.** `internal/release` implements the same
  strict, canonical manifest rules and Ed25519 domain-separated signature
  check. Both repositories pin the same test vector. Cloud verifies against
  `RELEASE_TRUSTED_PUBLIC_KEYS` (comma-separated base64).
  - This check is a filter only. Retail re-verifies against its own
    compiled-in keys and never trusts Cloud's list.
  - Import is fail-closed (`RELEASE_TRUST_NOT_CONFIGURED`) when no keys are
    configured.
- **No private key anywhere in Cloud.** There is no signing code path in
  the server, and no key column, environment variable or file.
- **Immutable storage (migration 00041).**
  - `releases` stores the exact envelope bytes, digest, sequence (unique),
    version, commit, `min_installed_sequence` and `key_id`.
  - `release_artifacts` stores the signed `(os, arch, package, file_name,
    size, sha256)` plus its operational URL.
  - Database triggers forbid updating signed columns and deleting rows.
  - Only `status` (ACTIVE/REVOKED) and its actor/time may change.
- **Idempotence and conflicts.**
  - Re-importing identical bytes returns the existing release (200).
  - The same sequence with a different digest is `409
    RELEASE_SEQUENCE_CONFLICT`.
- **Revocation.** REVOKED stops all new delivery, because commands are
  claimed only for ACTIVE releases.
  - A device that already received the command learns
    `release_status: REVOKED` from its next report acknowledgement.
  - If the device has not yet begun activation, it abandons the attempt
    (`UPDATE_RELEASE_REVOKED`).
  - A device already past its safe boundary finishes or rolls back
    locally.
  - Target rows are not rewritten. Reactivation (explicit, audited) lets
    still-pending targets be delivered again.
- **Artifact locations** must be `https://`. `http://` is allowed only when
  `ENVIRONMENT=development`.
- **Audit.** Every import, revoke and reactivate writes
  `update_audit_events` with the operator identity.

## Consequences

- A Cloud database or container compromise can withhold, delay, or
  re-offer existing validly signed releases. It cannot produce an
  installable release.
- Rotating keys requires updating `RELEASE_TRUSTED_PUBLIC_KEYS` in step
  with Retail releases (Retail `docs/operations/self-update.md` §3).

## Related documentation

- `docs/operations/fleet-updates.md`
- `api/openapi.yaml` (Phase 18 routes)
- ADR-0052
