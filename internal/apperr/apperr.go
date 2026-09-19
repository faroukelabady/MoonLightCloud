// Package apperr is the small domain/application error taxonomy.
// HTTP mapping lives in the http adapter; pgx errors never cross it.
package apperr

// Kind classifies application failures for consistent HTTP mapping.
type Kind int

const (
	InvalidInput Kind = iota + 1
	NotFound
	Unauthorized
	Forbidden
	Conflict
	Unavailable
	Internal
)

// Error is a classified application error.
type Error struct {
	Kind    Kind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Code is the stable wire code for the HTTP error envelope.
func (e *Error) Code() string {
	switch e.Kind {
	case InvalidInput:
		return "INVALID_INPUT"
	case NotFound:
		return "NOT_FOUND"
	case Unauthorized:
		return "UNAUTHORIZED"
	case Forbidden:
		return "FORBIDDEN"
	case Conflict:
		return "CONFLICT"
	case Unavailable:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

// New builds a classified error.
func New(k Kind, msg string) *Error { return &Error{Kind: k, Message: msg} }

// Wrap classifies an underlying error.
func Wrap(k Kind, msg string, err error) *Error { return &Error{Kind: k, Message: msg, Err: err} }
