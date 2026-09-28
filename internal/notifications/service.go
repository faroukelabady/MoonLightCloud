package notifications

import (
	"context"
	"log/slog"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Machine-readable dispatch outcome codes. Provider prose, recipients,
// and parameter values never enter these codes.
const (
	CodeTemplateMappingMissing    = "NOTIFICATION_TEMPLATE_MAPPING_MISSING"
	CodeTemplateParametersInvalid = "NOTIFICATION_TEMPLATE_PARAMETERS_INVALID"
	CodeProviderAuth              = "NOTIFICATION_PROVIDER_AUTH"
	CodeProviderRateLimited       = "NOTIFICATION_PROVIDER_RATE_LIMITED"
	CodeProviderRetry             = "NOTIFICATION_PROVIDER_RETRY"
	CodeSendAmbiguous             = "NOTIFICATION_SEND_AMBIGUOUS"
	CodeProviderValidation        = "NOTIFICATION_PROVIDER_VALIDATION"
	CodeProviderConflict          = "NOTIFICATION_PROVIDER_CONFLICT"
	CodeProviderUnknown           = "NOTIFICATION_PROVIDER_UNKNOWN"
	CodeDispatchError             = "NOTIFICATION_DISPATCH_ERROR"
)

// EnqueueResult is the structured outcome of one enqueue call.
type EnqueueResult struct {
	ID          string
	Created     bool
	ProviderKey string
	TemplateKey string
	Locale      string
}

// Service validates enqueue calls against durable template mappings
// and appends to the idempotent outbox. It performs no scheduling:
// the dispatcher and the enqueue CLI both call into it synchronously.
type Service struct {
	mappings MappingStore
	outbox   EnqueueStore
	log      *slog.Logger
}

// NewService wires enqueue over the mapping and outbox stores.
func NewService(mappings MappingStore, outbox EnqueueStore, log *slog.Logger) *Service {
	return &Service{mappings: mappings, outbox: outbox, log: log}
}

// EnqueueTemplate validates one provider-neutral template request,
// snapshots the resolved external mapping immutably, and appends the
// durable notification. Identical replays return the existing ID;
// contradictory replays conflict without mutating the original.
func (s *Service) EnqueueTemplate(ctx context.Context, req EnqueueTemplateRequest) (EnqueueResult, error) {
	key, err := ValidateProviderKey(req.ProviderKey)
	if err != nil {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := ValidateIdempotencyKey(req.IdempotencyKey); err != nil {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := ValidateRecipient(req.Recipient); err != nil {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := ValidateTemplateKey(req.TemplateKey); err != nil {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := ValidateLocale(req.Locale); err != nil {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := ValidateParameters(req.Parameters); err != nil {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	mapping, found, err := s.mappings.GetTemplateMapping(ctx, string(key), req.TemplateKey, req.Locale)
	if err != nil {
		return EnqueueResult{}, err
	}
	if !found {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, CodeTemplateMappingMissing)
	}
	if !mapping.Enabled {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, CodeTemplateMappingMissing)
	}
	if err := matchParameters(req.Parameters, mapping.ParameterNames); err != nil {
		return EnqueueResult{}, apperr.New(apperr.InvalidInput, CodeTemplateParametersInvalid)
	}
	intent := EnqueueIntent{
		ProviderKey: string(key), IdempotencyKey: req.IdempotencyKey,
		Recipient: req.Recipient, TemplateKey: req.TemplateKey, Locale: req.Locale,
		Parameters:      req.Parameters,
		ExtTemplateName: mapping.ExternalTemplateName, ExtLanguageCode: mapping.ExternalLanguageCode,
		ExtParameterOrder: append([]string(nil), mapping.ParameterNames...),
	}
	id, outcome, err := s.outbox.EnqueueNotification(ctx, intent)
	if err != nil {
		return EnqueueResult{}, err
	}
	s.logInfo("notification enqueued", "provider", string(key), "template", req.TemplateKey,
		"locale", req.Locale, "notification", id, "created", outcome == EnqueueInserted)
	return EnqueueResult{
		ID: id, Created: outcome == EnqueueInserted,
		ProviderKey: string(key), TemplateKey: req.TemplateKey, Locale: req.Locale,
	}, nil
}

// matchParameters enforces the exact parameter set: every mapped name
// present, no unknown extras. Deterministic templates only.
func matchParameters(parameters map[string]string, names []string) error {
	if len(parameters) != len(names) {
		return apperr.New(apperr.InvalidInput, CodeTemplateParametersInvalid)
	}
	for _, name := range names {
		if _, ok := parameters[name]; !ok {
			return apperr.New(apperr.InvalidInput, CodeTemplateParametersInvalid)
		}
	}
	return nil
}

func (s *Service) logInfo(msg string, args ...any) {
	if s.log != nil {
		s.log.Info(msg, args...)
	}
}
