# ADR-0034 — Notification Provider and WhatsApp Delivery

## Status

Accepted (Phase 7A implementation).

## Context

Phases 7B (scheduled business reports) and 7D (operational alerts)
need a reusable notification delivery primitive. Without a
provider-neutral boundary, each future phase would embed WhatsApp
API details, template names, and retry logic separately, repeating the
coupling Phase 6A removed from commerce.

## Decision

Introduce a provider-neutral notification domain with WhatsApp Cloud
API as the first adapter:

- **NotificationProvider** (`SendTemplate` only): template messages,
  no free-form text, no media, no interactivity. Future providers
  (email/SMS) implement the same interface.
- **Logical templates**: business logic names `template_key` +
  `locale` with named parameters. The durable
  `notification_template_mappings` table resolves them to approved
  external template names, language codes, and deterministic BODY
  parameter order. Template approval stays manual in Meta; MoonLight
  never creates, edits, or polls templates.
- **Durable idempotent outbox** (`notification_messages`) keyed by
  `(provider_key, idempotency_key)` with a semantic fingerprint over
  provider, recipient, template, locale, parameters, and the resolved
  external snapshot. Identical replays return the existing row;
  contradictory replays conflict. The external mapping is snapshotted
  at enqueue so later mapping edits cannot silently transform queued
  business messages.
- **Lease fencing** (Phase 6C pattern): every claim increments
  `lease_generation`; every finish requires owner + generation on a
  non-terminal row; stale workers update zero rows.
- **Remote side-effect ambiguity**: a durable send-start marker is
  written before provider HTTP. Any claim observing the marker
  without an explicit safe outcome converges to **ambiguous** without
  touching the network. Ambiguous is terminal for automatic retry:
  only a new explicit notification after human assessment may
  supersede it. Lease fencing alone cannot fix remote side effects —
  the provider may already have sent.
- **Strict acceptance**: a 2xx with anything other than exactly one
  accepted non-empty bounded provider message ID is ambiguous
  (including `held_for_quality_assessment`/`paused`, which block as
  validation since nothing was sent). Accepted messages are never
  automatically re-sent; provider `failed` delivery updates delivery
  state only.
- **Delivery tracking** (`notification_delivery_status_history`):
  provider timestamp is primary ordering evidence; same-timestamp
  ties resolve ACCEPTED < SENT < DELIVERED < READ with FAILED
  terminal-conservative; UNKNOWN records history without advancing
  current state. Correlation uses provider message IDs only, never
  recipient data.
- **Webhooks**: GET verification via verify token (constant-time,
  bounded challenge echo); POST via `X-Hub-Signature-256` hex HMAC
  over exact raw bytes with the app secret. Inbound customer content
  and unknown message IDs are acknowledged without persistence.
- **Credentials**: access token, app secret, and verify token are
  separate Cloud-only runtime configuration, never persisted, never
  logged, scrubbed before diagnostic bounding.

## R2 remediation (privacy, pacing, delivery ordering)

The 7A freeze review found one HIGH plus three MEDIUM defects, closed
without touching fencing, revisions, or the projection schema:

1. **Value-free config errors.** A malformed timeout echoed raw
   environment content into startup errors. All WhatsApp validation
   errors are now value-free by construction (key + rule only); no
   scrubbing layer is trusted with arbitrary config text.
2. **Machine-only provider errors.** Provider fields — prose AND
   numeric code/subcode, which can also reflect request-private
   values — never cross the adapter boundary. `NotificationError`
   carries a fixed MoonLight-owned diagnostic plus kind; HTTP status
   and Retry-After drive all behavior. Dispatcher logs, CLI output,
   and persisted `last_error_code` are machine codes by construction.
3. **ACCEPTED-baseline ordering.** Local API-acceptance time no
   longer participates in provider ordering: the first known provider
   callback always establishes provider state, then anchors normal
   timestamp ordering. Row locking from R1 is unchanged.
4. **Paced identity retention.** A valid provider message ID with
   `held_for_quality_assessment` (or any unfamiliar informational
   status) is accepted with the ID persisted — never blocked,
   retried, or discarded — so later delivery callbacks correlate and
   the send is never repeated. Missing/empty/multiple IDs stay
   ambiguous.
5. **Malformed status isolation.** Timestamps decode per entry; a
   non-numeric timestamp skips that entry without writes while valid
   siblings persist; all-malformed envelopes ack 200.
6. **Value-free CLI errors.** Malformed `--param` fails before flag
   parsing (whose `invalid value %q` wrapper would echo content).

## R3 remediation (provider diagnostic privacy)

Numeric-only validation of provider `code`/`error_subcode` is not a
privacy guarantee: an adversarial provider can reflect
request-private numeric values (recipient digits, report amounts,
identifiers, numeric credentials) into any returned field.
`NotificationError` therefore carries a fixed MoonLight-owned
diagnostic per error kind; HTTP status classification and Retry-After
(which come from protocol framing, not body content) are unchanged.
Provider-controlled diagnostic values are not exposed unless
transformed into a MoonLight-owned bounded semantic classification.

## Consequences

- 7B calls `EnqueueTemplate` directly with logical template keys; no
  WhatsApp imports in business logic.
- 7D observes ambiguous/failed/blocked states; no alert rules in 7A.
- No automatic business triggers exist in 7A: no domain event
  enqueues notifications. Manual operator testing uses CLI.
- No dashboard UI, no public send API, no bulk tooling, no retention
  purge in 7A.
