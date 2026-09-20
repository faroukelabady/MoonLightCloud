# Device Operations

Credential format: `<device-id>.<credential-id>.<secret-hex>` in the
`Authorization: Bearer` header. Raw secrets are shown once and never
recoverable; only salt + verifier persist (HMAC-SHA256 with the server
pepper, ADR-0015).

## Create

```bash
moonlight-cloud device create --name shop-main
```

Save the printed credential immediately. Prefer generated output over
`--secret` flags (nothing secret touches shell history or process lists).

## List (safe, no secrets)

```bash
moonlight-cloud device list
```

Shows id, name, status, creation, last seen, and active/revoked credential
counts. Never verifiers, salts-as-secrets, or peppers.

## Rotate

```bash
moonlight-cloud device rotate <device-id>
```

One transaction: new credential activated, all other active credentials
revoked. Old secret stops working; new secret shown once. If rotation
fails before commit, the old credential remains usable — the device is
never left with zero credentials by a partial op.

Lost response after commit (new secret never seen): do NOT stack more
rotations blindly. Run `device rotate` again deliberately — each rotation
invalidates the previous, so exactly one re-rotation restores a known-good
secret; record it this time.

## Revoke

```bash
moonlight-cloud device revoke <device-id>              # device + all credentials, immediate
moonlight-cloud device revoke-credential <credential-id> # one credential only
```

Revocation is authoritative per request (no auth cache). History
(`sync_events`, audit rows) is never deleted on revoke.

## Lost credential

Re-provision path: `device rotate <device-id>` issues a fresh credential
and kills the lost one. No recovery of the old secret exists by design.

## Compromised credential

1. `device rotate <device-id>` (or `revoke` the device if the endpoint is hostile).
2. Replay exposure is bounded: captured events replay as `already_accepted`
   (identical) or `409` (altered) — idempotency is the replay defense.
3. If the server pepper itself is suspected compromised, see pepper notes:
   changing `DEVICE_SECRET_PEPPER` invalidates all v1 verifiers (no online
   multi-pepper migration in Phase 1B); each device must re-rotate after the
   new pepper is deployed. `pepper_version` on credential rows exists to
   enable a controlled migration later.

## Re-enrollment

A replacement machine is a new device (`device create`), never a recycled
credential. Retire the old device with `revoke`.
