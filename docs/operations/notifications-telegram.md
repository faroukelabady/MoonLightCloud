# Notifications & Telegram (Phase 10)

Telegram Bot API as the second concrete `NotificationProvider`,
alongside WhatsApp, on the unchanged provider-neutral queue,
dispatcher, idempotency, and template-mapping architecture.
Outbound plain-text `sendMessage` only; no inbound processing.

## Bot prerequisites (manual, in Telegram)

- Create the bot with [@BotFather](https://t.me/BotFather); record the
  **bot token** (looks like `123456:ABC-DEF...`; use
  `<telegram-bot-token>` as the placeholder everywhere).
- Decide the destination: a numeric **chat ID** (user, group, or
  channel; group/channel IDs are negative) or a **@channel username**.
  The bot must be a member (and admin where posting requires it)
  before first send; MoonLight never joins chats or manages
  permissions.
- No Telegram-side template approval exists: Telegram renders the
  local single-body mapping described below.

MoonLight never calls `setWebhook`, `getUpdates`, or `deleteWebhook`,
creates no bot, rotates no token, and manages no channel membership.
These remain manual operational prerequisites.

## Cloud configuration

| Variable | Required | Rule |
|---|---|---|
| `NOTIFICATIONS_TELEGRAM_ENABLED` | — | default `false`: no provider, no credentials required |
| `NOTIFICATIONS_TELEGRAM_PROVIDER_KEY` | when enabled | logical instance key, e.g. `telegram-main` (future `telegram-ops` needs no redesign) |
| `NOTIFICATIONS_TELEGRAM_BOT_TOKEN` | when enabled | Bot API shape `digits:word` (`123456:ABC-DEF...`); URL-path credential; never URL-logged, never persisted, never sent to Retail |
| `NOTIFICATIONS_TELEGRAM_BASE_URL` | — | default `https://api.telegram.org`; https only, no userinfo/query/fragment |
| `NOTIFICATIONS_TELEGRAM_HTTP_TIMEOUT` | — | default 15s, within 1s..120s |

Startup performs zero network calls: invalid syntax fails fast with
value-free errors; wrong-but-valid credentials surface at send time
as blocked auth failures. Enabling never requires Telegram
reachability. Disabled Telegram never interferes with WhatsApp
startup, and a duplicate logical key across providers fails startup
safely (no partial registration).

Token rotation is restart-based with no DB migration: replace the
runtime `NOTIFICATIONS_TELEGRAM_BOT_TOKEN`, restart Cloud. Queued
notifications reference the provider key, never a snapshotted
secret, so rotation never orphans queued work. A revoked token
blocks notifications with the auth code (no flood); generic 7D
notification incidents may observe the blocked row like any other
provider failure.

## Recipients

Report recipients (`provider_key` + address + locale) and
operational recipients (`provider_key` + recipient + locale) stay
authoritative. A Telegram destination is one of:

- numeric chat ID: `123456789`, `-1001234567890` (sign preserved,
  string end to end — never a JavaScript number);
- `@username`: `@operations` (5..32 ASCII letters, digits or underscores
  after `@`, 33 characters total; schema 26 is required).

Validation is deterministic and rewrites nothing: empty,
whitespace-edged, control-bearing, overlong, and malformed shapes are
rejected by generic enqueue validation. The provider-specific send gate
additionally rejects all numeric zero representations (`0`, `-00`, etc.)
before network access; valid nonzero bytes are preserved. Unknown-but-shaped destinations pass
validation and block terminally at send (Telegram answers
`chat not found`); they are never retried in a hot loop.

There is no global `TELEGRAM_CHAT_ID` bypass: every report or alert
recipient selects `provider_key = telegram-main` explicitly.

## Template mapping CLI

Telegram mappings reuse the generic command. The external name
selects the LOCAL renderer `telegram_text_v1` (stored in the legacy
physical column for compatibility; it is not a Telegram-hosted
template), and exactly one body parameter carries the fully composed
text — report and alert formatting are never duplicated in Telegram:

```bash
moonlight-cloud notifications template-map set \
  --provider telegram-main \
  --template daily_business_report_v1 \
  --locale ar \
  --external-name telegram_text_v1 \
  --language ar \
  --params report_body

moonlight-cloud notifications template-map set \
  --provider telegram-main \
  --template operational_alert_open_v1 \
  --locale ar \
  --external-name telegram_text_v1 \
  --language ar \
  --params alert_body

moonlight-cloud notifications template-map list --provider telegram-main
```

Mappings are durable Cloud state. Editing a mapping after enqueue
never alters the queued snapshot; the next enqueue sees the new
mapping. Missing or disabled mappings block before any provider
call. No `parse_mode` is ever sent: Arabic/English Unicode travels
byte-faithful plain text, and bodies over the 4096-character Bot API
bound block instead of truncating.

## Operator test enqueue

```bash
moonlight-cloud notifications enqueue \
  --provider telegram-main \
  --to @operations \
  --template operator_test_v1 \
  --locale ar \
  --param body="hello" \
  --idempotency-key manual-tg-001

moonlight-cloud notifications status --id <uuid>
```

CLI output never echoes recipients, parameter contents, or secrets.
Repeat the identical command for the idempotent same-ID result;
changed payload under the same key conflicts.

## Report recipients

```bash
moonlight-cloud business-reports recipients add \
  --label ops-tg --provider telegram-main \
  --recipient @operations --locale ar
```

Daily and ten-day reports enqueue one notification per recipient;
WhatsApp and Telegram recipients for the same slot share the
canonical computation and dispatch independently — a Telegram
failure never corrupts WhatsApp delivery.

## Operational recipients

Operational alert recipients are administered through the existing
recipient commands with `--provider telegram-main`. Open and
resolved alerts enqueue `operational_alert_open_v1` /
`operational_alert_resolved_v1` with the frozen `alert_body`; the
`ops-alert:` idempotency prefix keeps Telegram operational
notifications inside the frozen recursion exclusion (a blocked
Telegram alert never spawns a notification incident cycle).

## Dispatch statuses

`pending` → `retry` (explicit safe outcomes only: proven
before-write transport failure, 429 honoring `retry_after`, 5xx)
→ `accepted` (usable chat-qualified identity `<chat-id>:<message-id>`,
terminal) | `blocked` (terminal: auth, validation, migration,
bot-blocked) | `ambiguous` (unknown remote outcome, terminal for
automatic retry — human assessment, then a NEW notification).

Accepted means Telegram accepted `sendMessage`. It is recorded as
delivery `ACCEPTED` at most: MoonLight never fabricates
`DELIVERED`/`READ` — Telegram supplies no delivery callbacks in this
phase. `message_id` 0 (ephemeral/unsendable) cannot prove durable
uniqueness and ambiguates. A `migrate_to_chat_id` response blocks
with a stable operator-action code and never rewrites the
recipient; moving the destination is an explicit operator step.

Retries use 10s-doubling backoff capped at 1h; valid `retry_after`
hints (capped at 1h, overflow-safe) schedule the retry. The adapter
never sleeps: the dispatcher schedules.

## Troubleshooting

- `NOTIFICATION_PROVIDER_AUTH`: token invalid/revoked — rotate the
  runtime token and restart; queued rows reference the key, not the
  secret.
- `NOTIFICATION_PROVIDER_VALIDATION` on send: destination unknown,
  bot blocked/kicked, or insufficient rights — fix membership or the
  recipient value; the row stays blocked (no hot-loop).
- `telegram chat migrated; operator action required`: the group
  became a supergroup — register the new destination explicitly;
  MoonLight never auto-rewrites recipients.
- `NOTIFICATION_SEND_AMBIGUOUS`: the send may have reached Telegram
  (timeout/disconnect/malformed success after write, crash after
  send-start, DB failure after acceptance). Do not resend the same
  row; issue a new notification with a new idempotency key after
  human assessment.
- Redirect responses: refused without following so the path-bound
  token never leaves the configured origin; a redirecting base URL
  blocks terminally (fix the base URL).
