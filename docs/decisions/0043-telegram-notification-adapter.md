# ADR-0043: Telegram Notification Adapter (Phase 10)

Date: 2026-10-02
Status: accepted (Phase 10 implementation; freeze review pending)

## Context

Phase 7A built a provider-neutral notification architecture
(`NotificationProvider` with `Key()` + `SendTemplate()`, durable
generic queue, semantic idempotency, template-mapping snapshots,
lease-fenced dispatcher with send-start ambiguity protection) proven
by one adapter (WhatsApp). Phase 7B (scheduled reports) and Phase 7D
(operational alerts) enqueue through `notifications.Service` knowing
only provider key, recipient, template key, locale, and named
parameters. Phase 9 (multi-Store) deliberately left notification
integration state globally scoped.

Phase 10 adds Telegram as the second concrete provider with no new
notification subsystem, no Store ownership change, no Retail change,
and no commerce coupling. The original Phase 10 candidate is
`63c3c619b1bd92bd9acd0e7d8ccf7ebd2b89bec5`; it already contains
Phase 10. The actual Phase 9 ancestor is
`d7deb309c55a1da7918336fb323369ed109fcbe5`.

Phase 10-R1 raises the schema from 25 to 26 with append-only migration
00026 so the documented maximum username fits all recipient constraints.

Bot API verification (official docs, fetched 2026-10-02; Bot API
10.3, latest changelog August 24, 2026): `POST
https://api.telegram.org/bot<token>/sendMessage` with
`{chat_id: Integer|String, text: 1-4096 chars}`; success
`{ok:true, result: Message}` with chat-scoped `message_id` (0 =
ephemeral/unsendable); failure `{ok:false, error_code, description,
parameters?: {migrate_to_chat_id, retry_after}}`; chat IDs fit 52
bits (signed int64 safe); `error_code` contents are volatile.

## Decision

Reuse `NotificationProvider` with a Telegram adapter at
`internal/notifications/telegram/` (HTTP DTOs private), plain-text
outbound `sendMessage` only, no inbound processing, no webhook
route, no polling, no media.

Recipient union (minimal STOP-justified shared change): the generic
enqueue gate accepts E.164, Telegram numeric chat IDs (optional `-`,
up to 16 digits — the full 52-bit space, keeping the pre-existing
17-digit rejection), and `@username` (5..32 ASCII letters, digits or underscores after `@`,
33 characters including `@`). The official
[Telegram username policy](https://core.telegram.org/method/account.checkUsername)
defines the 5..32 character body. Each adapter enforces its own strict
subset at send time (WhatsApp keeps strict E.164, Telegram its own
validator, rejecting every numeric zero representation without rewriting
valid destination bytes), so widening enqueue acceptance can never misdirect
a send: unknown shapes block terminally at dispatch. Frozen
provider tests were expanded, not weakened.

Token-in-URL risk: full outbound URLs are secret-bearing — never
logged, persisted, error-embedded, or metric-labelled; redirects are
refused (`ErrUseLastResponse`, tested same- and cross-origin with
zero destination hits); token validation rejects control bytes
before URL construction.

Local renderer `telegram_text_v1` (named in the legacy physical
external-template column for compatibility, documented as local):
exactly one ordered body parameter travels verbatim as plain text,
no `parse_mode`, 4096-rune bound enforced before any network call
(never truncate). Report/alert formatting is never duplicated.

Chat-qualified identity `<actual-chat-id>:<message-id>` from the
returned (not requested) chat: same message_id in different chats
cannot collide; `message_id` 0 ambiguates. Success records
dispatch `accepted` / delivery `ACCEPTED` at most — no
DELIVERED/READ fabrication, no delivery-history callbacks.

Remote-send ambiguity is terminal and never auto-resent
(after-write disconnect/timeout, malformed success, crash after
send-start, DB failure after acceptance), reusing the frozen
dispatcher untouched. 429 honors bounded `retry_after` (min/normal/
huge/overflow/negative/missing/header-fallback); 401 blocks as auth;
400/403/404 block as validation; 408/5xx retry; chat migration
blocks with a stable operator-action code and never rewrites the
recipient.

7B/7D integration is configuration-only: Telegram recipients on
existing recipient models, same logical templates and locales, same
idempotency keys. The `ops-alert:` prefix keeps Telegram operational
notifications inside the frozen recursion exclusion. Bot token is
Cloud-only runtime configuration; Retail is untouched.

## Consequences

WhatsApp request format, auth, webhooks, callbacks, ordering, and
ambiguity semantics are unchanged (frozen 7A regressions green).
R1 migration 00026 widens notification, report-recipient and operational-
recipient length checks to 33. Delivery snapshot columns already use
unbounded text. Downgrade refuses atomically while any widened recipient
exceeds 32 characters; it never truncates or rewrites history. No `go.mod`,
OpenAPI or dashboard change. Multi-provider registry holds both adapters with duplicate
keys failing startup safely; the canonical dispatcher serves both
with per-row independent outcomes.

R1 requires explicit, typed envelope evidence: incomplete or contradictory
HTTP 2xx responses, duplicate envelope fields, malformed success identities,
trailing JSON/garbage and oversized responses are ambiguous. Supported
complete failures retain their existing classes. Unrelated future fields
remain allowed. Durable send-start evidence survives dispatcher and process
restart; ambiguous sends are never automatically repeated.
