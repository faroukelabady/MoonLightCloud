package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// Telegram Bot API contract, verified against the official Bot API
// documentation on 2026-10-02 (Bot API 10.3, latest changelog entry
// August 24, 2026):
//
//   - Requests: POST https://api.telegram.org/bot<token>/METHOD_NAME,
//     application/json, UTF-8. All queries must be served over HTTPS.
//   - sendMessage parameters: chat_id (Integer or String @username,
//     required), text (1-4096 characters after entities parsing,
//     required). No parse_mode in Phase 10.
//   - Success: {"ok":true,"result":{...Message...}}; Message.message_id
//     is unique inside its chat, 0 when ephemeral or scheduled-and-yet
//     unusable. Chat.id fits in 52 bits (signed int64 safe).
//   - Failure: {"ok":false,"error_code":N,"description":"...",
//     "parameters":{"migrate_to_chat_id"?, "retry_after"?}}.
//     error_code contents are subject to change; description is
//     human-readable prose and must never cross the adapter boundary.
//
// This adapter implements outbound plain-text sendMessage only. No
// inbound updates, no webhooks, no polling, no media.

// RendererBodyV1 is the local/provider renderer selector stored (for
// compatibility) in the legacy physical external_template_name column.
// It is NOT a Telegram-hosted template: Telegram has no provider-side
// approved-template requirement. The renderer sends exactly one
// ordered parameter value as plain text, verbatim.
const RendererBodyV1 = "telegram_text_v1"

// maxResponseBytes bounds Bot API response/error bodies before decoding.
const maxResponseBytes = 1024 * 1024

// maxRetryAfter bounds accepted retry_after metadata.
const maxRetryAfter = time.Hour

// maxTextRunes is the sendMessage text bound (characters after
// entities parsing). MoonLight enqueue bounds (1024 bytes per value)
// are strictly tighter; this is defense-in-depth so a future
// parameter relaxation can never silently truncate financial or
// operational content: oversize blocks instead.
const maxTextRunes = 4096

var (
	// telegramNumericPattern mirrors the generic enqueue union:
	// optional -, then up to 16 digits (full 52-bit Bot API space).
	telegramNumericPattern = regexp.MustCompile(`^-?[0-9]{1,16}$`)
	// telegramUsernamePattern mirrors the generic enqueue union: @
	// plus 5..31 word characters (32-column durable bound).
	telegramUsernamePattern = regexp.MustCompile(`^@[A-Za-z0-9_]{5,31}$`)
)

// ValidateRecipient is the deterministic Telegram recipient
// validator/canonicalizer. Accepted forms (Bot API chat_id):
// numeric chat ID (negative group/channel IDs preserved) or
// @username. Canonicalization is identity: exact bytes preserved,
// no rewriting, no number coercion (IDs stay strings end to end).
// Empty, whitespace-edged, control-bearing, overlong, zero, and
// malformed values are rejected; one chat is never transformed into
// another.
func ValidateRecipient(recipient string) (string, error) {
	switch {
	case telegramNumericPattern.MatchString(recipient):
		if recipient == "0" || recipient == "-0" {
			return "", fmt.Errorf("invalid telegram recipient: chat ID zero is never valid")
		}
		if _, err := strconv.ParseInt(recipient, 10, 64); err != nil {
			return "", fmt.Errorf("invalid telegram recipient: want a numeric chat ID or @username")
		}
		return recipient, nil
	case telegramUsernamePattern.MatchString(recipient):
		return recipient, nil
	}
	return "", fmt.Errorf("invalid telegram recipient: want a numeric chat ID or @username")
}

// Provider is the Telegram Bot API template sender. Plain-text
// outbound sendMessage only, through the existing logical-template
// abstraction. Construction validates configuration eagerly and
// performs no network I/O.
type Provider struct {
	key      notifications.ProviderKey
	baseURL  string
	botToken string
	http     *http.Client
}

// NewProvider builds the adapter from validated configuration. A nil
// httpClient gets production transport with redirect refusal and the
// configured timeout; tests inject a TLS stub transport. The bot
// token is held in memory only: never logged, never persisted.
func NewProvider(cfg config.TelegramNotificationConfig, httpClient *http.Client) (*Provider, error) {
	key, err := notifications.ValidateProviderKey(cfg.ProviderKey)
	if err != nil {
		return nil, err
	}
	// The token travels in the request URL path, so control bytes
	// are rejected here (not only at env-load validation): a raw
	// control byte must never reach URL construction, where parse
	// errors could echo secret-bearing text.
	if len(cfg.BotToken) == 0 || len(cfg.BotToken) > 256 {
		return nil, fmt.Errorf("telegram provider requires a bot token")
	}
	for i := 0; i < len(cfg.BotToken); i++ {
		if cfg.BotToken[i] < 32 || cfg.BotToken[i] == 127 {
			return nil, fmt.Errorf("telegram provider requires a bounded non-control bot token")
		}
	}
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	if client.Timeout <= 0 {
		client.Timeout = config.DefaultTelegramHTTPTimeout
	}
	if httpClient != nil {
		client.Transport = httpClient.Transport
		client.Jar = httpClient.Jar
	}
	// Redirects are refused: the bot token lives in the request URL,
	// so a followed redirect would leak the secret to another origin.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Provider{
		key: key, baseURL: cfg.NormalizedBaseURL(),
		botToken: cfg.BotToken, http: client,
	}, nil
}

// Key returns the logical provider instance key, never the vendor name.
func (p *Provider) Key() notifications.ProviderKey {
	return p.key
}

// sendMessageRequest is the exact Bot API send shape for Phase 10:
// chat_id plus plain text. No parse_mode (byte-faithful localized
// text), no reply markup, no media. chat_id travels as a string in
// both numeric and @username forms so exact identity survives without
// float coercion; the Bot API accepts both Integer and String.
type sendMessageRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// botAPIResponse is the Bot API envelope. Decoding is permissive by
// design (no unknown-field rejection): the API gains Message fields
// over time and only ok/result/error_code/parameters drive behavior.
type botAPIResponse struct {
	OK          bool                `json:"ok"`
	Result      *sendMessageResult  `json:"result,omitempty"`
	ErrorCode   int                 `json:"error_code,omitempty"`
	Description string              `json:"description,omitempty"`
	Parameters  *responseParameters `json:"parameters,omitempty"`
}

// sendMessageResult carries the success evidence Phase 10 needs:
// the actual returned chat identity and the chat-scoped message ID.
type sendMessageResult struct {
	MessageID int64        `json:"message_id"`
	Chat      *messageChat `json:"chat"`
}

type messageChat struct {
	ID int64 `json:"id"`
}

// responseParameters carries machine-actionable error metadata.
type responseParameters struct {
	MigrateToChatID *int64 `json:"migrate_to_chat_id,omitempty"`
	RetryAfter      *int64 `json:"retry_after,omitempty"`
}

// SendTemplate delivers one plain-text message. The request resolves
// the enqueue-time snapshot (never live mapping state). Success means
// Telegram accepted sendMessage and returned a usable chat-qualified
// message identity: dispatch_status becomes accepted (delivery
// ACCEPTED at most; MoonLight never fabricates DELIVERED/READ).
// Every uncertain outcome is Ambiguous and never auto-resent.
func (p *Provider) SendTemplate(ctx context.Context, req notifications.TemplateSendRequest) (notifications.SendResult, error) {
	if ctx.Err() != nil {
		return notifications.SendResult{}, ctx.Err()
	}
	body, err := p.renderRequest(req)
	if err != nil {
		return notifications.SendResult{}, err
	}
	// The full URL is secret-bearing (token in path): it is built
	// here, used once, and never logged, persisted, or embedded in
	// any error, metric, or trace.
	target := p.baseURL + "/bot" + p.botToken + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return notifications.SendResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "moonlight-cloud-notifications/1.0")
	var wroteRequest bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest = true },
	}
	request = request.WithContext(httptrace.WithClientTrace(ctx, trace))
	response, err := p.http.Do(request)
	if err != nil {
		if isRedirectRefused(err, response) {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			// Deterministic for the same payload and config: a
			// redirecting origin can never process sendMessage,
			// so nothing was sent and retrying cannot succeed.
			// Blocked (operator fixes the base URL), and the
			// token never left the configured origin.
			return notifications.SendResult{}, notifications.ValidationError("telegram request refused: redirect")
		}
		return notifications.SendResult{}, classifyTransport(ctx, err, wroteRequest)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		// Belt-and-braces: an injected client that follows
		// redirects anyway must not be trusted with the token URL.
		return notifications.SendResult{}, notifications.ValidationError("telegram request refused: redirect")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return notifications.SendResult{}, notifications.AmbiguousError("telegram response read failed")
	}
	if int64(len(raw)) > maxResponseBytes {
		return notifications.SendResult{}, notifications.AmbiguousError("telegram response exceeds body limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return notifications.SendResult{}, classifyStatus(response.StatusCode,
			response.Header.Get("Retry-After"), decodeErrorParameters(raw))
	}
	return acceptResponse(raw)
}

// isRedirectRefused reports a refused redirect: Go surfaces both the
// 3xx response and a url.Error wrapping ErrUseLastResponse.
func isRedirectRefused(err error, response *http.Response) bool {
	if !errors.Is(err, http.ErrUseLastResponse) {
		return false
	}
	if response == nil {
		return true
	}
	return response.StatusCode >= 300 && response.StatusCode < 400
}

// renderRequest builds the exact sendMessage body from the snapshotted
// resolution. Parameter values travel verbatim as JSON text.
func (p *Provider) renderRequest(req notifications.TemplateSendRequest) ([]byte, error) {
	if req.ProviderKey != p.key {
		return nil, notifications.ValidationError("template request addressed to another provider instance")
	}
	recipient, err := ValidateRecipient(req.Recipient)
	if err != nil {
		return nil, notifications.ValidationError(err.Error())
	}
	resolved := req.Resolved
	if resolved.ExternalTemplateName != RendererBodyV1 {
		// The legacy physical column names a LOCAL renderer for
		// Telegram, never a Telegram-hosted template. Unknown
		// renderer selectors block: MoonLight must not guess prose.
		return nil, notifications.ValidationError("unknown telegram renderer")
	}
	if len(resolved.ParameterOrder) != 1 {
		return nil, notifications.ValidationError("telegram text renderer requires exactly one body parameter")
	}
	name := resolved.ParameterOrder[0]
	text, ok := resolved.Parameters[name]
	if !ok || text == "" {
		return nil, notifications.ValidationError("telegram message body missing")
	}
	if len([]rune(text)) > maxTextRunes {
		// Never truncate financial or operational content.
		return nil, notifications.ValidationError("telegram message body exceeds provider bound")
	}
	// HTML escaping stays off so &,<,> travel as literal UTF-8 bytes:
	// plain text must never be reinterpreted as formatting, and the
	// wire form stays byte-faithful to the localized message.
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(sendMessageRequest{ChatID: recipient, Text: text}); err != nil {
		return nil, notifications.ValidationError("telegram request encoding failed")
	}
	return bytes.TrimRight(encoded.Bytes(), "\n"), nil
}

// acceptResponse enforces the success invariant: ok:true with a usable
// result (actual chat identity plus a nonzero chat-scoped message ID).
// Anything else after the request may have been sent is AMBIGUOUS:
// Telegram may already own the message, so MoonLight must not resend.
func acceptResponse(raw []byte) (notifications.SendResult, error) {
	var parsed botAPIResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&parsed); err != nil {
		return notifications.SendResult{}, notifications.AmbiguousError("telegram success response malformed")
	}
	if !parsed.OK {
		return notifications.SendResult{}, classifyStatus(parsed.ErrorCode, "", parsed.Parameters)
	}
	result := parsed.Result
	if result == nil || result.Chat == nil {
		return notifications.SendResult{}, notifications.AmbiguousError("telegram success without message result")
	}
	if result.Chat.ID == 0 {
		return notifications.SendResult{}, notifications.AmbiguousError("telegram success without chat identity")
	}
	if result.MessageID == 0 {
		// message_id 0 marks ephemeral or not-yet-sendable
		// messages: no durable uniqueness, so no acceptance.
		return notifications.SendResult{}, notifications.AmbiguousError("telegram success without usable message identity")
	}
	return notifications.SendResult{ProviderMessageID: providerMessageID(result.Chat.ID, result.MessageID)}, nil
}

// providerMessageID builds the deterministic composite identity
// <chat-id>:<message-id> from the ACTUAL returned chat identity (not
// the requested recipient string). Telegram message IDs are
// chat-scoped, so the bare message ID must never be stored where
// uniqueness is enforced per provider: the same message_id in two
// chats yields two distinct identities. Both halves are canonical
// integers, so last-colon parsing is unambiguous and no escaping is
// needed. Bounded (~40 chars) and free of recipient PII beyond the
// numeric chat identity the store already requires for correlation.
func providerMessageID(chatID, messageID int64) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(chatID, 10))
	b.WriteByte(':')
	b.WriteString(strconv.FormatInt(messageID, 10))
	return b.String()
}

// decodeErrorParameters extracts machine-actionable error metadata
// from a non-2xx body without ever surfacing provider prose. A body
// that does not decode simply yields no parameters; the HTTP status
// still drives classification.
func decodeErrorParameters(raw []byte) *responseParameters {
	var parsed botAPIResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&parsed); err != nil {
		return nil
	}
	return parsed.Parameters
}

// classifyStatus maps Bot API outcomes to the frozen notification
// taxonomy. Only fixed MoonLight-owned strings cross the boundary:
// description prose may contain destination/provider data and is
// classified (never persisted, never logged). error_code contents are
// volatile per Telegram docs; status plus parameters drive behavior,
// with a small stable description sniff for terminal destination
// failures.
func classifyStatus(status int, retryAfterHeader string, params *responseParameters) *notifications.NotificationError {
	const message = "telegram provider rejected request"
	switch {
	case status == http.StatusUnauthorized:
		// Invalid or revoked bot token: non-retryable auth.
		return notifications.AuthenticationError(message)
	case status == http.StatusRequestTimeout:
		return notifications.TemporaryError(message)
	case status == http.StatusTooManyRequests:
		return notifications.RateLimitedError(message, parseRetryAfter(retryAfterHeader, params))
	case status == http.StatusConflict:
		return notifications.ConflictError(message)
	case status == http.StatusBadRequest || status == http.StatusNotFound ||
		status == http.StatusForbidden || status == http.StatusUnprocessableEntity:
		return classifyClientRejection(params)
	case status >= 500:
		return notifications.TemporaryError(message)
	case status >= 400 && status < 500:
		// Unclassified 4xx is a permanent provider rejection:
		// the request was received and refused, so retrying the
		// same payload cannot succeed.
		return notifications.ValidationError(message)
	default:
		return notifications.TemporaryError(message)
	}
}

// classifyClientRejection maps definitive 4xx rejections. Chat
// migration never rewrites a report/alert recipient: it blocks with
// a stable operator-action code and the mutation requires an explicit
// operator step. All destination failures are terminal (no hot-loop):
// blocked, never retried.
func classifyClientRejection(params *responseParameters) *notifications.NotificationError {
	if params != nil && params.MigrateToChatID != nil {
		return notifications.ValidationError("telegram chat migrated; operator action required")
	}
	return notifications.ValidationError("telegram provider rejected request")
}

// parseRetryAfter bounds retry metadata: parameters.retry_after
// first (Bot API flood control), then the Retry-After header, else no
// hint (dispatcher backoff applies). Negative, missing, and
// unparseable values yield no hint; huge values cap at one hour so the
// dispatcher never sleeps past its own horizon. Never sleeps here:
// the dispatcher schedules the retry.
func parseRetryAfter(header string, params *responseParameters) time.Duration {
	if params != nil && params.RetryAfter != nil {
		return boundRetryAfter(*params.RetryAfter)
	}
	if v := strings.TrimSpace(header); v != "" {
		if seconds, err := strconv.ParseInt(v, 10, 64); err == nil {
			return boundRetryAfter(seconds)
		}
		if when, err := http.ParseTime(v); err == nil {
			delay := time.Until(when)
			if delay < 0 {
				return 0
			}
			if delay > maxRetryAfter {
				return maxRetryAfter
			}
			return delay
		}
	}
	return 0
}

// boundRetryAfter clamps a seconds hint into [0, maxRetryAfter].
// The comparison precedes the multiplication so absurd hints can
// never overflow the duration arithmetic.
func boundRetryAfter(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds > int64(maxRetryAfter/time.Second) {
		return maxRetryAfter
	}
	return time.Duration(seconds) * time.Second
}

// classifyTransport separates proven before-write failures (safe
// retry) from possibly-written ones (ambiguous). Caller cancellation
// is preserved unwrapped, never recast as a generic failure.
func classifyTransport(ctx context.Context, err error, wroteRequest bool) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if !wroteRequest {
		return notifications.TemporaryError("telegram request never written")
	}
	return notifications.AmbiguousError("telegram request outcome unknown")
}
