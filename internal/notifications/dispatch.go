package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DispatchStatus is the durable send-queue state of one notification.
// Dispatch never overloads provider delivery outcomes.
type DispatchStatus string

const (
	// DispatchPending means queued, never sent.
	DispatchPending DispatchStatus = "pending"
	// DispatchRetry means an explicit safely-retryable provider outcome
	// authorized another send attempt.
	DispatchRetry DispatchStatus = "retry"
	// DispatchAccepted means the provider returned exactly one valid
	// message ID. Terminal for sending: never automatically re-sent.
	DispatchAccepted DispatchStatus = "accepted"
	// DispatchBlocked means a terminal non-retryable failure.
	DispatchBlocked DispatchStatus = "blocked"
	// DispatchAmbiguous means the send outcome is unknown (the request
	// may have reached the provider). Terminal for automatic retry:
	// only a new explicit notification may supersede it.
	DispatchAmbiguous DispatchStatus = "ambiguous"
)

// DeliveryStatus is the canonical provider delivery state, tracked
// separately from dispatch. ACCEPTED means Meta accepted the request;
// it does NOT mean the recipient received anything.
type DeliveryStatus string

const (
	// DeliveryUnknown covers unmapped provider statuses.
	DeliveryUnknown DeliveryStatus = "UNKNOWN"
	// DeliveryAccepted means the provider acknowledged the send.
	DeliveryAccepted DeliveryStatus = "ACCEPTED"
	// DeliverySent means the provider sent the message.
	DeliverySent DeliveryStatus = "SENT"
	// DeliveryDelivered means the recipient device received it.
	DeliveryDelivered DeliveryStatus = "DELIVERED"
	// DeliveryRead means the recipient read it.
	DeliveryRead DeliveryStatus = "READ"
	// DeliveryFailed means provider-reported failure. A delivery
	// outcome only: it never authorizes another business message.
	DeliveryFailed DeliveryStatus = "FAILED"
)

// deliveryPrecedence orders canonical statuses for same-timestamp
// determinism. FAILED beats non-FAILED at equal timestamps (a failed
// delivery must not hide behind a same-instant lesser state); UNKNOWN
// never advances current state.
func deliveryPrecedence(status DeliveryStatus) int {
	switch status {
	case DeliveryAccepted:
		return 1
	case DeliverySent:
		return 2
	case DeliveryDelivered:
		return 3
	case DeliveryRead:
		return 4
	case DeliveryFailed:
		return 5
	default:
		return 0
	}
}

// DeliveryPrecedence exposes the documented same-timestamp order for
// tests and operators: ACCEPTED < SENT < DELIVERED < READ, FAILED
// terminal-conservative, UNKNOWN never advancing.
func DeliveryPrecedence(status DeliveryStatus) int {
	return deliveryPrecedence(status)
}

// MapProviderStatus normalizes a raw provider status string. Unknown
// values map to UNKNOWN with the raw value preserved by the caller.
func MapProviderStatus(raw string) DeliveryStatus {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "accepted":
		return DeliveryAccepted
	case "sent":
		return DeliverySent
	case "delivered":
		return DeliveryDelivered
	case "read":
		return DeliveryRead
	case "failed", "undelivered":
		return DeliveryFailed
	default:
		return DeliveryUnknown
	}
}

var (
	templateKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)
	localePattern      = regexp.MustCompile(`^[a-z]{2}([-_][A-Za-z]{2,8})?$`)
	paramKeyPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	// recipientPattern is the canonical WhatsApp recipient: optional
	// leading +, then 7..15 digits. No country inference, no national
	// rewriting: callers supply international identity as-is.
	recipientPattern = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
	// telegramRecipientNumericPattern is the Telegram numeric chat
	// identity: optional leading - (group/channel/supergroup IDs are
	// negative), then up to 16 digits. Sixteen digits cover the full
	// Bot API 52-bit chat-ID space; the pre-existing 17-digit rejection
	// stays rejected.
	telegramRecipientNumericPattern = regexp.MustCompile(`^-?[0-9]{1,16}$`)
	// telegramRecipientUsernamePattern is the Telegram @username chat
	// identity: @ plus 5..31 word characters. The 31-character body
	// cap (not Telegram's 32) keeps every accepted recipient within
	// the durable 32-character recipient column; a 32-character
	// username must be registered by numeric chat ID instead.
	telegramRecipientUsernamePattern = regexp.MustCompile(`^@[A-Za-z0-9_]{5,31}$`)
	// idempotencyKeyPattern bounds caller identity without PII rules:
	// printable ASCII, no controls, no whitespace edges.
	idempotencyKeyPattern = regexp.MustCompile(`^[!-~]{1,128}$`)
)

// Parameter bounds keep queue payloads deterministic and Meta-safe.
const (
	MaxTemplateParameters = 32
	MaxParameterValueLen  = 1024
	MaxParametersTotal    = 16384
)

// ValidateTemplateKey rejects empty or unsafe logical template keys.
func ValidateTemplateKey(key string) error {
	if !templateKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid template key %q: use 1..64 lowercase letters, numbers, underscore", key)
	}
	return nil
}

// ValidateLocale rejects empty or unsafe locale tags.
func ValidateLocale(locale string) error {
	if !localePattern.MatchString(locale) {
		return fmt.Errorf("invalid locale %q: want ll or ll_CC", locale)
	}
	return nil
}

// ValidateRecipient accepts the provider-neutral recipient union:
// E.164 international identity (WhatsApp) or Telegram numeric chat
// identity / @username (Phase 10). This is the minimal STOP-justified
// Phase 10 capability change: enqueue must admit Telegram-shaped
// recipients, and recipient administration (reports, operations, CLI)
// shares this gate. Each ADAPTER still enforces its own strict subset
// at send time (WhatsApp keeps strict E.164; Telegram keeps its own
// canonicalizer), so widening enqueue acceptance can never cause a
// misdirected send: unknown shapes block terminally at dispatch.
// No guessing, no rewriting.
func ValidateRecipient(recipient string) error {
	switch {
	case recipientPattern.MatchString(recipient):
		return nil
	case telegramRecipientNumericPattern.MatchString(recipient):
		return nil
	case telegramRecipientUsernamePattern.MatchString(recipient):
		return nil
	}
	return fmt.Errorf("invalid recipient: want E.164 digits, Telegram numeric chat ID, or @username")
}

// ValidateWhatsAppRecipient enforces the frozen WhatsApp send-time
// subset: optional leading +, then 7..15 digits. The WhatsApp adapter
// calls this (not the widened union) so Phase 7A send semantics are
// unchanged by the Phase 10 recipient union.
func ValidateWhatsAppRecipient(recipient string) error {
	if !recipientPattern.MatchString(recipient) {
		return fmt.Errorf("invalid recipient: want optional + followed by 7..15 digits")
	}
	return nil
}

// ValidateIdempotencyKey rejects empty, overlong, or unsafe caller
// keys. Recipient PII must never be used as an idempotency key.
func ValidateIdempotencyKey(key string) error {
	if !idempotencyKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid idempotency key: use 1..128 printable ASCII characters")
	}
	return nil
}

// ValidateParameters enforces exact bounded parameter sets: keys are
// ASCII identifiers, values bounded, aggregate bounded.
func ValidateParameters(parameters map[string]string) error {
	if len(parameters) > MaxTemplateParameters {
		return fmt.Errorf("too many parameters: %d exceeds %d", len(parameters), MaxTemplateParameters)
	}
	total := 0
	for key, value := range parameters {
		if !paramKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid parameter name %q", key)
		}
		if len(value) > MaxParameterValueLen {
			return fmt.Errorf("parameter %q exceeds %d characters", key, MaxParameterValueLen)
		}
		total += len(value)
	}
	if total > MaxParametersTotal {
		return fmt.Errorf("parameters exceed %d aggregate bytes", MaxParametersTotal)
	}
	return nil
}

// EnqueueTemplateRequest is the provider-neutral enqueue call future
// phases invoke directly. No Meta names appear here.
type EnqueueTemplateRequest struct {
	ProviderKey    string
	IdempotencyKey string
	Recipient      string
	TemplateKey    string
	Locale         string
	Parameters     map[string]string
}

// FingerprintSemantic computes the deterministic semantic identity:
// provider, recipient, template key, locale, canonicalized parameters,
// and the resolved external template snapshot. UUIDs, attempts,
// leases, timestamps, message IDs, and delivery state are excluded.
// Parameter map order cannot affect the result: names are sorted.
func FingerprintSemantic(providerKey, recipient, templateKey, locale string, parameters map[string]string, resolved ResolvedTemplate) [32]byte {
	names := make([]string, 0, len(parameters))
	for name := range parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	writer := sha256.New()
	writer.Write([]byte("notification.v1\x00"))
	for _, field := range []string{providerKey, recipient, templateKey, locale} {
		writer.Write([]byte(field))
		writer.Write([]byte{0})
	}
	for _, name := range names {
		writer.Write([]byte(name))
		writer.Write([]byte{0})
		writer.Write([]byte(parameters[name]))
		writer.Write([]byte{0})
	}
	for _, field := range []string{
		resolved.ExternalTemplateName, resolved.ExternalLanguageCode,
		strings.Join(resolved.ParameterOrder, ","),
	} {
		writer.Write([]byte(field))
		writer.Write([]byte{0})
	}
	var sum [32]byte
	copy(sum[:], writer.Sum(nil))
	return sum
}

// FingerprintHex renders a semantic fingerprint for storage/logging
// shape tests. Fingerprints carry no PII beyond a one-way digest.
func FingerprintHex(sum [32]byte) string {
	return hex.EncodeToString(sum[:])
}

// DeliveryEventFingerprint is the deterministic status-event identity:
// provider, message ID, raw status, canonical status, timestamp, and
// error code. Duplicate provider callbacks dedupe; genuine transitions
// differ.
func DeliveryEventFingerprint(providerKey, providerMessageID, rawStatus string, canonical DeliveryStatus, providerTimestamp *time.Time, errorCode string) [32]byte {
	writer := sha256.New()
	writer.Write([]byte("notification-status.v1\x00"))
	for _, field := range []string{providerKey, providerMessageID, rawStatus, string(canonical), errorCode} {
		writer.Write([]byte(field))
		writer.Write([]byte{0})
	}
	if providerTimestamp != nil {
		writer.Write([]byte(providerTimestamp.UTC().Format(time.RFC3339Nano)))
	}
	writer.Write([]byte{0})
	var sum [32]byte
	copy(sum[:], writer.Sum(nil))
	return sum
}

// AdvanceDelivery decides whether an event moves current delivery
// state. Provider timestamp is primary ordering evidence; UNKNOWN
// records history only and never advances; equal timestamps resolve by
// documented precedence with FAILED terminal-conservative.
//
// ACCEPTED is the local API-acceptance baseline, not provider
// ordering evidence: the first known provider callback (SENT,
// DELIVERED, READ, FAILED) always establishes provider ordering
// state, even when its provider timestamp predates local persistence.
// That callback's timestamp then anchors all subsequent provider
// ordering. This never compares local and provider clocks as peers.
func AdvanceDelivery(current DeliveryStatus, currentAt *time.Time, event DeliveryStatus, eventAt *time.Time) bool {
	if event == DeliveryUnknown {
		return false
	}
	if eventAt == nil {
		return false
	}
	if current == DeliveryAccepted {
		return true
	}
	if currentAt == nil {
		return true
	}
	if eventAt.After(*currentAt) {
		return true
	}
	if eventAt.Equal(*currentAt) && deliveryPrecedence(event) > deliveryPrecedence(current) {
		return true
	}
	return false
}
