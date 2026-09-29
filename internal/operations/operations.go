// Package operations owns the Phase 7D operational incident domain:
// durable incidents over frozen notification/report/device-control state,
// provider-neutral alert deliveries, and one narrow reconnect recovery.
// No WhatsApp/vendor types here; no sync/report business logic.
package operations

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Rule keys: exactly the v1 detector vocabulary. No generic rule engine.
const (
	RuleDeviceOffline       = "DEVICE_OFFLINE"
	RuleDeviceSyncFailed    = "DEVICE_SYNC_FAILED"
	RuleDeviceSyncStale     = "DEVICE_SYNC_STALE"
	RuleReportBlocked       = "BUSINESS_REPORT_BLOCKED"
	RuleReportStale         = "BUSINESS_REPORT_STALE"
	RuleNotificationBlocked = "NOTIFICATION_BLOCKED"
	RuleNotificationAmb     = "NOTIFICATION_AMBIGUOUS"
	RuleNotificationStale   = "NOTIFICATION_RETRY_STALE"
)

// Subject types for incident identity.
const (
	SubjectDevice       = "device"
	SubjectSyncCommand  = "sync_command"
	SubjectReportRun    = "report_run"
	SubjectNotification = "notification"
)

// Severities use operator language only (never Critical/High/Medium).
const (
	SeverityWarning = "warning"
	SeverityUrgent  = "urgent"
)

// Incident states.
const (
	StateOpen         = "open"
	StateAcknowledged = "acknowledged"
	StateResolved     = "resolved"
)

// Alert event types: opened + resolved only. Acknowledgement never sends.
const (
	EventOpened   = "opened"
	EventResolved = "resolved"
)

// Logical Phase 7A template keys with the single alert_body parameter.
const (
	TemplateOpen     = "operational_alert_open_v1"
	TemplateResolved = "operational_alert_resolved_v1"
	ParamAlertBody   = "alert_body"
)

// MaxAlertBodyBytes keeps the alert_body parameter inside the Phase 7A
// 1024-byte parameter limit with margin for envelope encoding.
const MaxAlertBodyBytes = 900

// Recovery action vocabulary: exactly one automatic action in v1.
const (
	ActionReconnectSync = "DEVICE_RECONNECT_SYNC"
	// RecoveryResultExisting records healing satisfied without a new command.
	RecoveryResultExisting = "satisfied_by_existing_command"
	// RecoveryResultCreated records a newly queued command.
	RecoveryResultCreated = "sync_command_created"
)

// Bounded resolution codes.
const (
	ResolutionReconnect = "RECONNECT_RESOLVED"
	ResolutionNoActive  = "DEVICE_NO_LONGER_ACTIVE"
	ResolutionCleared   = "CONDITION_CLEARED"
	ResolutionOperator  = "OPERATOR_RESOLVED"
	ResolutionTerminal  = "TERMINAL_REACHED"
)

// Bounded alert-delivery error codes.
const (
	CodeAlertEnqueued    = "OPS_ALERT_ENQUEUED"
	CodeNoMapping        = "OPS_ALERT_NO_MAPPING"
	CodeBodyTooLarge     = "OPS_ALERT_BODY_TOO_LARGE"
	CodeEnqueueFailed    = "OPS_ALERT_ENQUEUE_FAILED"
	CodeNoRecipients     = "OPS_ALERT_NO_RECIPIENTS"
	CodeRecoveryQueued   = "OPS_RECOVERY_QUEUED"
	CodeRecoveryExisting = "OPS_RECOVERY_EXISTING"
	CodeRecoveryFailed   = "OPS_RECOVERY_FAILED"
)

// Incident is the durable operator incident.
type Incident struct {
	ID             string
	Rule           string
	SubjectType    string
	SubjectID      string
	Severity       string
	State          string
	Episode        int
	SourceEventKey *string
	OpenedAt       time.Time
	LastObservedAt time.Time
	AcknowledgedAt *time.Time
	ResolvedAt     *time.Time
	ResolutionCode *string
	// OpenIntentMaterialized distinguishes an intentionally empty opened
	// snapshot (true, zero deliveries) from a missing one (false, repair).
	OpenIntentMaterialized bool
	// ResolvedIntentMaterialized is the same distinction for the resolved event.
	ResolvedIntentMaterialized bool
}

// RecoveryIntent carries an optional reconnect-recovery request into an
// atomic resolve transaction.
type RecoveryIntent struct {
	DeviceID string
	Key      string
}

// IsStateful reports rules whose condition can naturally clear.
func IsStateful(rule string) bool {
	switch rule {
	case RuleDeviceOffline, RuleDeviceSyncStale, RuleReportStale, RuleNotificationStale:
		return true
	}
	return false
}

// SeverityFor assigns v1 severities: urgent for ambiguous delivery and
// failed sync execution; warning otherwise.
func SeverityFor(rule string) string {
	switch rule {
	case RuleNotificationAmb, RuleDeviceSyncFailed:
		return SeverityUrgent
	}
	return SeverityWarning
}

// ValidateRule rejects unknown rule keys (closed vocabulary).
func ValidateRule(rule string) error {
	switch rule {
	case RuleDeviceOffline, RuleDeviceSyncFailed, RuleDeviceSyncStale,
		RuleReportBlocked, RuleReportStale,
		RuleNotificationBlocked, RuleNotificationAmb, RuleNotificationStale:
		return nil
	}
	return apperr.New(apperr.InvalidInput, "unknown operations rule")
}

// ValidateLabel enforces safe operator labels: bounded, no newlines or
// control characters (log/body/dashboard injection defense).
func ValidateLabel(label string) error {
	if len(label) == 0 || len(label) > 64 {
		return apperr.New(apperr.InvalidInput, "invalid operations label")
	}
	for i := 0; i < len(label); i++ {
		if c := label[i]; c < 0x20 || c == 0x7f {
			return apperr.New(apperr.InvalidInput, "invalid operations label")
		}
	}
	return nil
}

// SafeSubject renders an operator-safe subject token: bounded identifier
// plus sanitized label, never raw payloads.
func SafeSubject(id, label string) string {
	label = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, label)
	if len(label) > 48 {
		label = label[:48]
	}
	if strings.TrimSpace(label) == "" {
		return id
	}
	return id + " (" + label + ")"
}

// AlertBody composes the compact deterministic bilingual body. All inputs
// are bounded machine values; bodies exceeding the parameter limit are
// rejected, never truncated into misleading content.
func AlertBody(locale, event, rule, subject string, at time.Time, extra string) (string, error) {
	ts := at.UTC().Format(time.RFC3339)
	var body string
	if locale == "ar" {
		body = "تنبيه تشغيلي\nالنوع: " + rule + "\nالموضوع: " + subject + "\nالحدث: " + event + "\nالوقت: " + ts
	} else {
		body = "Operational alert\nRule: " + rule + "\nSubject: " + subject + "\nEvent: " + event + "\nTime: " + ts
	}
	if extra != "" {
		if locale == "ar" {
			body += "\nملاحظة: " + extra
		} else {
			body += "\nNote: " + extra
		}
	}
	if len([]byte(body)) > MaxAlertBodyBytes {
		return "", apperr.New(apperr.InvalidInput, CodeBodyTooLarge)
	}
	return body, nil
}

// Fingerprint binds delivery identity to rule/incident/event/locale/body.
func Fingerprint(rule, incidentID, event, locale, body string) []byte {
	sum := sha256.Sum256([]byte(strings.Join([]string{rule, incidentID, event, locale, body}, "\x00")))
	return sum[:]
}

// AlertIdempotencyKey derives the deterministic Phase 7A key: incident +
// event + delivery UUIDs only. No recipient, body, or PII inside the key.
func AlertIdempotencyKey(incidentID, event, deliveryID string) string {
	return fmt.Sprintf("ops-alert:%s:%s:%s", incidentID, event, deliveryID)
}

// ReconnectIdempotencyKey derives the deterministic Phase 7C key for one
// offline incident: at most one command per incident by construction.
func ReconnectIdempotencyKey(incidentID string) string {
	return fmt.Sprintf("ops-reconnect-sync:%s", incidentID)
}

// OpsOwnedKey reports Phase 7D-owned Phase 7A notifications, which the
// notification-failure detectors must exclude to prevent alert storms.
func OpsOwnedKey(idempotencyKey, templateKey string) bool {
	return strings.HasPrefix(idempotencyKey, "ops-alert:") ||
		strings.HasPrefix(templateKey, "operational_alert_")
}

// EncodeCursor builds an opaque keyset cursor from page position.
func EncodeCursor(at time.Time, id string) string {
	raw := at.UTC().Format(time.RFC3339Nano) + "|" + id
	dst := make([]byte, 0, len(raw)*2)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ':' || c == '.' {
			dst = append(dst, c)
		} else {
			dst = append(dst, '%')
			dst = append(dst, "0123456789ABCDEF"[c>>4], "0123456789ABCDEF"[c&15])
		}
	}
	return string(dst)
}

// DecodeCursor parses a cursor produced by EncodeCursor.
func DecodeCursor(cursor string) (time.Time, string, error) {
	out := make([]byte, 0, len(cursor))
	for i := 0; i < len(cursor); i++ {
		if cursor[i] == '%' {
			if i+2 >= len(cursor) {
				return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid page cursor")
			}
			hi := strings.IndexByte("0123456789ABCDEF", cursor[i+1])
			lo := strings.IndexByte("0123456789ABCDEF", cursor[i+2])
			if hi < 0 || lo < 0 {
				return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid page cursor")
			}
			out = append(out, byte(hi<<4|lo))
			i += 2
			continue
		}
		out = append(out, cursor[i])
	}
	parts := strings.SplitN(string(out), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid page cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid page cursor")
	}
	return at.UTC(), parts[1], nil
}
