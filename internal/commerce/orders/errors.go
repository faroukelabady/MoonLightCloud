package orders

import (
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Machine-readable reconciliation failure codes. Persisted in queue
// state instead of prose: no credentials, no PII, no response bodies.
const (
	CodeOrderNotFound            = "ORDER_NOT_FOUND"
	CodeOrderCurrencyUnsupported = "ORDER_CURRENCY_UNSUPPORTED"
	CodeOrderInvalid             = "ORDER_INVALID"
	CodeOrderConflict            = "ORDER_CONFLICT"
	CodeOrderProviderAuth        = "ORDER_PROVIDER_AUTH"
	CodeOrderProviderInvalid     = "ORDER_PROVIDER_INVALID"
	CodeOrderReconcileError      = "ORDER_RECONCILE_ERROR"
)

// BlockedError is a terminal reconciliation failure carrying a stable
// persisted code and a safe operator message.
type BlockedError struct {
	Code    string
	Message string
}

func (e *BlockedError) Error() string { return e.Code + ": " + e.Message }

// IsBlocked reports whether err is terminal for the same desired state.
func IsBlocked(err error) (string, bool) {
	var blocked *BlockedError
	if errors.As(err, &blocked) {
		return blocked.Code, true
	}
	return "", false
}

func asProviderError(err error, target **commerce.ProviderError) bool {
	return errors.As(err, target)
}
