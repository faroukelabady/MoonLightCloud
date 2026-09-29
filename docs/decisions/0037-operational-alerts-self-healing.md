# ADR-0037: Operational Alerts & Bounded Self-Healing (Phase 7D)

## Context

Phase 7C delivers truthful connectivity and a durable Sync Now channel,
but operators have no durable incident surface: offline devices, failed
syncs, blocked reports, and ambiguous notifications are visible only in
logs. Phase 7D adds detection, deduplication, provider-neutral alerting,
and one narrow automatic recovery without becoming a generic automation
engine.

## Decision

- **Incident model:** durable `operational_incidents` with
  `open/acknowledged/resolved`, `warning/urgent` severities (operator
  language, never review severities), stateful vs event rules, episode
  recurrence, stable `source_event_key` dedupe, and partial-unique
  active constraints so concurrent detectors converge.
- **Alert delivery:** per-recipient immutable snapshots (provider,
  recipient, locale, template, body, fingerprint) enqueued through the
  frozen Phase 7A `EnqueueTemplate` boundary only — never the WhatsApp
  adapter, never provider `SendTemplate`. Deterministic keys
  `ops-alert:<incident>:<event>:<delivery>` adopt the same notification
  after crashes. Templates `operational_alert_open_v1` /
  `operational_alert_resolved_v1` with a single `alert_body` parameter.
- **Recursion prevention:** 7D-owned notifications carry the
  `ops-alert:` idempotency prefix (and `operational_alert_*` templates);
  notification-failure detectors exclude them in SQL. Missing mappings
  block deliveries (dashboard-visible) instead of failing incidents.
- **Self-healing:** exactly one automatic action,
  `DEVICE_RECONNECT_SYNC`, armed once per resolved offline incident and
  executed through the frozen Phase 7C command service with the
  deterministic key `ops-reconnect-sync:<incident>`. Existing active
  commands satisfy the action; manual/auto races converge on the 7C
  one-active invariant; failed auto commands open normal failure
  incidents without recursion. Disabled by default.
- **Why no resends:** ambiguous outcomes are uncertain by definition
  (resending may double a provider mutation); blocked reports/notifications
  and failed syncs need human assessment. 7D alerts only.
- **Dashboard:** incident list (bounded keyset pages, state/severity/rule
  filters), acknowledge (never resolves), guarded resolve (409 while a
  stateful condition holds), masked deliveries, recovery visibility, and a
  batched device rollup (one aggregate query — F11 is not worsened).

## Consequences

- New migration 00019 (Cloud target 19); Retail untouched.
- Alerting and healing are independently gated (`OPERATIONS_ENABLED`,
  `OPERATIONS_AUTO_SYNC_ON_RECONNECT`, both default false).
- Phase 8 owns fleet scale, retention, and any broader healing.
