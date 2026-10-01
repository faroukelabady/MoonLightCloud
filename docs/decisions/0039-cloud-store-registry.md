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
  (device auth, tight body bound): unbound devices bind with
  first-writer-wins convergence; bound devices re-presenting the
  same store converge idempotently (metadata last-writer-wins);
  bound devices presenting another store get deterministic
  `STORE_BINDING_CONFLICT`. Authorization rests on the
  operator-provisioned device credential, never on store-UUID
  knowledge. Revoked devices are rejected by existing auth before
  registration logic runs, and revocation never touches stores,
  bindings, or history.
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
