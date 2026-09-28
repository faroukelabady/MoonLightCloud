// Package businessreports owns the Phase 7B scheduled business-report
// domain: durable recipients, Cairo-calendar schedules, slot
// materialization, immutable report snapshots, and delivery records
// that enqueue through the frozen Phase 7A provider-neutral
// notification service. Financial calculations stay in the frozen
// reporting domain: this package only resolves periods, formats
// deterministic localized bodies, and orchestrates durability. No
// WhatsApp imports, no provider HTTP, no schedules beyond DAILY and
// TEN_DAY, no automatic business triggers beyond configured schedules.
package businessreports

import (
	"fmt"
	"regexp"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// ReportKind is the scheduled report cadence. Exactly two exist.
type ReportKind string

const (
	// ReportDaily covers one previous complete Cairo calendar day.
	ReportDaily ReportKind = "DAILY"
	// ReportTenDay covers ten previous complete Cairo calendar days.
	ReportTenDay ReportKind = "TEN_DAY"
)

// RunKind distinguishes scheduled slots from operator manual runs.
type RunKind string

const (
	// RunScheduled is one materialized schedule slot.
	RunScheduled RunKind = "scheduled"
	// RunManual is one operator run-now invocation.
	RunManual RunKind = "manual"
)

// RunStatus is the durable report-run state. Completed means every
// intended Phase 7A notification is durably enqueued — never that
// WhatsApp delivered anything.
type RunStatus string

const (
	// RunPending means queued, never processed.
	RunPending RunStatus = "pending"
	// RunRetry means a temporary local failure: another attempt later.
	RunRetry RunStatus = "retry"
	// RunCompleted means all deliveries enqueued. Terminal.
	RunCompleted RunStatus = "completed"
	// RunBlocked means a deterministic non-retryable issue. Terminal.
	RunBlocked RunStatus = "blocked"
)

// DeliveryStatus is one report delivery's enqueue state. Temporary
// failures stay pending while the run retries; only terminal outcomes
// move it.
type DeliveryStatus string

const (
	// DeliveryPending means not yet enqueued.
	DeliveryPending DeliveryStatus = "pending"
	// DeliveryEnqueued means durably handed to Phase 7A. Terminal.
	DeliveryEnqueued DeliveryStatus = "enqueued"
	// DeliveryBlocked means permanently un-sendable. Terminal.
	DeliveryBlocked DeliveryStatus = "blocked"
)

// Stable machine codes for run/delivery outcomes. Provider prose,
// recipients, and report contents never enter these codes.
const (
	CodeNoRecipients               = "REPORT_NO_RECIPIENTS"
	CodeBodyTooLarge               = "REPORT_BODY_TOO_LARGE"
	CodeCompositionFailed          = "REPORT_COMPOSITION_FAILED"
	CodeNotificationMappingMissing = "REPORT_NOTIFICATION_MAPPING_MISSING"
	CodeNotificationValidation     = "REPORT_NOTIFICATION_VALIDATION"
	CodeNotificationIdemConflict   = "REPORT_NOTIFICATION_IDEMPOTENCY_CONFLICT"
	CodeInternalRetry              = "REPORT_INTERNAL_RETRY"
)

// TimezoneCairo is the only Phase 7B scheduling zone.
const TimezoneCairo = "Africa/Cairo"

// Logical notification template keys. 7B never knows Meta names.
const (
	TemplateDaily  = "daily_business_report_v1"
	TemplateTenDay = "ten_day_business_report_v1"
)

// TemplateKeyForKind resolves the logical template for a report kind.
func TemplateKeyForKind(kind ReportKind) string {
	if kind == ReportTenDay {
		return TemplateTenDay
	}
	return TemplateDaily
}

// Supported recipient locales. Arabic is primary.
const (
	LocaleAR = "ar"
	LocaleEN = "en"
)

var (
	localTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	civilDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// ValidateReportKind rejects unknown cadences.
func ValidateReportKind(kind string) (ReportKind, error) {
	switch ReportKind(kind) {
	case ReportDaily, ReportTenDay:
		return ReportKind(kind), nil
	default:
		return "", apperr.New(apperr.InvalidInput, "invalid report kind: want DAILY|TEN_DAY")
	}
}

// ValidateLocalTime rejects malformed HH:MM wall-clock times.
func ValidateLocalTime(value string) error {
	if !localTimePattern.MatchString(value) {
		return apperr.New(apperr.InvalidInput, "invalid local time: want HH:MM")
	}
	return nil
}

// ValidateLocale rejects unknown recipient locales.
func ValidateLocale(locale string) error {
	if locale != LocaleAR && locale != LocaleEN {
		return apperr.New(apperr.InvalidInput, "invalid locale: want ar|en")
	}
	return nil
}

// ValidateCivilDate rejects malformed or nonexistent calendar dates.
func ValidateCivilDate(value string) error {
	if !civilDatePattern.MatchString(value) {
		return apperr.New(apperr.InvalidInput, "invalid date: want YYYY-MM-DD")
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return apperr.New(apperr.InvalidInput, "invalid date: want YYYY-MM-DD")
	}
	return nil
}

// NotificationIdempotencyKey derives the deterministic Phase 7A key
// for one delivery. UUIDs only: no recipient, no body, no PII.
func NotificationIdempotencyKey(runID, deliveryID string) string {
	return fmt.Sprintf("business-report:%s:%s", runID, deliveryID)
}
