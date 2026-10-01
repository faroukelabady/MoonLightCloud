# ADR-0039: Cloud Store Registry & Ingress Store Context

## Status

Accepted (Phase 9A, Cloud side).

## Context

Cloud knew devices but had no store concept, so multi-device shops
could not be grouped and future phases could not scope data by
store. Retail now owns a stable Store ID that must be registered,
bound to devices, and snapshotted at ingress without touching frozen
domain event contracts.

## Decision

- Minimal registry: `stores` (id, display_name, timezone,
  active|disabled) plus immutable `device_store_bindings`
  (device PK → store, one binding per device enforced by PRIMARY
  KEY, stores may have many devices). No tenant/organization model.
- Registration endpoint `POST /api/v1/sync/store-registration`
  authenticates through existing device credentials. An unbound active
  device may atomically bootstrap a genuinely new Store. Joining an existing
  Store requires explicit durable enrollment by the trusted operator CLI:
  `moonlight-cloud device enroll-store <device-id> <store-id>`.
  Enrollment validates existing Store and active device, replays matching
  bindings idempotently, and rejects immutable binding conflicts. A generic
  join attempt returns 409 `STORE_ENROLLMENT_REQUIRED` before metadata changes.
  The Store insert affected-row count rejects concurrent bootstrap losers;
  registration/enrollment serialize on the active device row in PostgreSQL.
  Bound devices re-presenting their Store converge metadata idempotently;
  another Store returns `STORE_BINDING_CONFLICT`. Revocation never deletes
  Store identity, bindings, or historical ingress context.
- Ingress context: `sync_events.store_id` is set server-side from
  the authenticated device binding at ACK, NULL for unbound/legacy
  devices, and never rewritten afterwards (replays keep the
  snapshotted row). Domain payloads are unchanged — no v3/v2
  contract churn.
- Capabilities advertise `store_registration: true`; old clients
  ignore it. Dashboard shows per-device store context (unbound
  truthfully empty) and a read-only store list with aggregate
  device counts. Connectivity still derives from devices only.
- Migration 00022 is append-only; v21 data upgrades with NULL
  store context and zero fabricated stores/bindings.

## Consequences

- Rollout requires Cloud support before Retail emits registration,
  but old Retail syncs unchanged and new Retail tolerates old
  Cloud. Cross-store spoofing is structurally impossible (context
  derives from binding, never from payload claims).
- No business projections are re-scoped yet; later phases build on
  the immutable ingress context.
