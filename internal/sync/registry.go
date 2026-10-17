package sync

import (
	"encoding/json"
	"sort"
)

const DiagnosticEventType = "system.test.v1"

type Validator = func(json.RawMessage) error

// Registry is an immutable snapshot of one service's accepted event contracts.
// Its map is private and constructors copy caller-owned definitions. Validators
// must themselves be safe for concurrent use, as the business validators are.
type Registry struct{ validators map[string]Validator }

func NewRegistry(definitions map[string]Validator) Registry {
	entries := make(map[string]Validator, len(definitions))
	for name, validate := range definitions {
		if validate == nil && name != DiagnosticEventType {
			panic("sync registry requires a validator for each business event")
		}
		entries[name] = validate
	}
	return Registry{validators: entries}
}

// DiagnosticRegistry explicitly enables the transport-only diagnostic event.
// It has no business projector and no additional payload validator. Production
// composition chooses whether to include the same diagnostic capability.
func DiagnosticRegistry() Registry {
	return NewRegistry(map[string]Validator{DiagnosticEventType: nil})
}

func (r Registry) eventTypes() []string {
	names := make([]string, 0, len(r.validators))
	for name := range r.validators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
