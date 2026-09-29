# Notifications & WhatsApp (Phase 7A)

Provider-neutral notification delivery with WhatsApp Cloud API as the
first adapter. Template messages only; no automatic business triggers
in 7A — enqueue happens through explicit service calls or operator CLI.

## Meta prerequisites (manual, in Meta)

- WhatsApp Business Account with a Cloud API phone number.
- Record the **Phone Number ID** (API path identity, not a recipient).
- Create and get approval for each message template; record the
  **external template name** and **language code**.
- Generate a long-lived **access token** (system user).
- Note the app **App Secret** (POST HMAC key, not the verify token).
- Choose a **verify token** (GET callback verification, not HMAC).
- Operational assumption: the configured token is scoped so
  `messaging_account_id` is not required (single-account sends).
  Multi-account routing is out of scope for Phase 7A.

MoonLight never manages template approval lifecycle.

## Cloud configuration

| Variable | Required | Rule |
|---|---|---|
| `NOTIFICATIONS_WHATSAPP_ENABLED` | — | default `false`: no provider, no webhook route, no credentials required |
| `NOTIFICATIONS_WHATSAPP_PROVIDER_KEY` | when enabled | logical instance key, e.g. `whatsapp-main` |
| `NOTIFICATIONS_WHATSAPP_GRAPH_VERSION` | when enabled | explicit `vXX.X`; API-version retirement stays operationally visible |
| `NOTIFICATIONS_WHATSAPP_BASE_URL` | — | default `https://graph.facebook.com`; https only, no userinfo/query/fragment |
| `NOTIFICATIONS_WHATSAPP_PHONE_NUMBER_ID` | when enabled | 1..32 digits; belongs in the API path |
| `NOTIFICATIONS_WHATSAPP_ACCESS_TOKEN` | when enabled | Bearer header only; never URL, logs, DB, or dashboard |
| `NOTIFICATIONS_WHATSAPP_APP_SECRET` | when enabled | POST HMAC key; never persisted |
| `NOTIFICATIONS_WHATSAPP_WEBHOOK_VERIFY_TOKEN` | when enabled | GET verification; never persisted |
| `NOTIFICATIONS_WHATSAPP_HTTP_TIMEOUT` | — | default 15s, within 1s..120s |

Startup performs zero network calls: invalid syntax fails fast with
value-free errors; wrong-but-valid credentials surface at send time.
Enabling never requires Meta reachability.

## Webhook setup (manual, in Meta)

1. Callback URL:
   `https://<cloud>/api/v1/notifications/webhooks/whatsapp/<provider_key>`
2. Verify token: the configured webhook verify token (GET flow).
3. Subscribe the WABA `messages` field (status callbacks).
4. The app secret signs POST bodies (`X-Hub-Signature-256`); MoonLight
   never subscribes anything automatically and needs no management
   permissions at runtime.

The three credentials are distinct concepts: access token (outbound),
app secret (POST HMAC), verify token (GET verification). Restart-based
rotation is acceptable: update config, restart Cloud, coordinate Meta
webhook settings as applicable.

## Template mapping CLI

```bash
moonlight-cloud notifications template-map set \
  --provider whatsapp-main \
  --template operator_test_v1 \
  --locale ar \
  --external-name moonlight_operator_test_ar \
  --language ar \
  --params name,message

moonlight-cloud notifications template-map list --provider whatsapp-main
```

Mappings are durable Cloud state (survive every rebuild, require
backup). Prefer `enabled=false` over deletion. Parameters must match
exactly: all mapped names present, no extras.

## Operator test enqueue

```bash
moonlight-cloud notifications enqueue \
  --provider whatsapp-main \
  --to 201012345678 \
  --template operator_test_v1 \
  --locale ar \
  --param name="Moon Light" \
  --param message="hello" \
  --idempotency-key manual-test-001

moonlight-cloud notifications status --id <uuid>
```

Recipients are canonical international identity (`+` optional,
7..15 digits, sent verbatim — MoonLight never infers country codes).
CLI output never echoes recipients, parameter contents, or secrets.
Enqueue creates durable work; the dispatcher sends asynchronously.
Repeat the identical command for the idempotent same-ID result;
changed payload under the same key conflicts.

## Dispatch statuses

`pending` (queued) → `retry` (explicit safe provider outcome only) →
`accepted` (exactly one provider message ID, terminal for sending) |
`blocked` (terminal failure) | `ambiguous` (unknown remote outcome,
terminal for automatic retry — human assessment, then a NEW
notification with a NEW idempotency key).

Safe retry happens only on explicit provider evidence (408, 429
honoring Retry-After, 5xx, proven before-write transport failure)
with 10s-doubling backoff capped at 1h. Anything unknown after send
start is ambiguous, never retried. Unclassified permanent HTTP 4xx
responses are terminal validation failures (blocked), never retried:
only 408/429 among 4xx are retryable. Provider diagnostics are
machine-only (fixed MoonLight-owned text per error kind — numeric
Meta codes included, since any provider-controlled field can reflect
request-private values): provider content never reaches errors, logs,
CLI, or persisted codes, so reflected recipients or parameters cannot
leak.

## Paced sends

Meta may accept a send but hold delivery
(`held_for_quality_assessment`, `paused`, or future informational
states). MoonLight persists the returned provider message ID and
marks dispatch accepted: the send is never repeated, and later
`sent`/`delivered`/`read`/`failed` callbacks correlate normally.
A 2xx without exactly one valid ID stays ambiguous.

## Delivery statuses

`ACCEPTED` (local API-acceptance baseline, not provider ordering
evidence) → provider callbacks `SENT` → `DELIVERED` → `READ`, plus
provider `FAILED` (delivery outcome only — never resends) and
`UNKNOWN` (raw preserved, current state untouched). The first known
provider callback always establishes provider ordering state even if
its timestamp predates local acceptance; afterwards out-of-order
callbacks resolve by provider timestamp; same-timestamp ties resolve
ACCEPTED < SENT < DELIVERED < READ with FAILED conservative.

## Status webhook behavior

Signed callbacks (`sent`/`delivered`/`read`/`failed`) update history
and current delivery state atomically by provider message ID.
Duplicates dedupe; unknown IDs and inbound customer messages are
acknowledged without persistence; failed statuses never resend.
If the database cannot persist, the webhook returns 5xx so Meta
redelivers.

Retained from each callback: provider message identity and delivery
status/timestamp. Discarded at the boundary: provider-controlled
error diagnostics (`code`, `error_subcode`, `message`, `title`,
`details`, `error_user_msg`) — history `provider_error_code` stays
NULL for webhook-applied events. Historical rows are never rewritten
or purged by this policy.

## Secret rotation

Update the corresponding variable and restart Cloud. No dynamic
reload. Coordinate Meta webhook settings when rotating the verify
token or app secret.

## Backup significance

`notification_template_mappings`, `notification_messages`, and
`notification_delivery_status_history` are durable Cloud-only
integration state (like provider mappings): back them up; no
catalog/policy/inventory/order rebuild touches them; no automatic
retention purge exists.

## Troubleshooting

- `NOTIFICATION_TEMPLATE_MAPPING_MISSING`: create the mapping first;
  no provider call was made.
- `NOTIFICATION_TEMPLATE_PARAMETERS_INVALID`: exact parameter set
  mismatch; compare with `template-map list`.
- `NOTIFICATION_PROVIDER_AUTH`: check the access token (valid syntax,
  invalid grant); Cloud still starts.
- `NOTIFICATION_PROVIDER_RATE_LIMITED`: throttled; backoff honors
  Retry-After automatically.
- `NOTIFICATION_SEND_AMBIGUOUS`: outcome unknown — inspect Meta-side
  before creating a NEW notification; never assumed duplicate-safe.
- Webhook 401s: clock skew does not matter for HMAC; check the app
  secret and exact raw-body forwarding through proxies.
