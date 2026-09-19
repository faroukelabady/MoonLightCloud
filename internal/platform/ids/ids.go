// Package ids abstracts UUID generation where deterministic tests need it.
// Production uses crypto-random UUIDv7 (time-ordered, good index locality).
package ids

import "github.com/google/uuid"

// Generator creates UUID strings.
type Generator interface {
	New() string
}

// System is the production generator (UUIDv7, crypto-random).
type System struct{}

// New returns a fresh UUIDv7 string, falling back to random UUID.
func (System) New() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// Fixed is a deterministic test generator cycling canned values.
type Fixed struct {
	Values []string
	next   int
}

// New returns the next canned value.
func (f *Fixed) New() string {
	if len(f.Values) == 0 {
		return "00000000-0000-7000-8000-000000000000"
	}
	v := f.Values[f.next%len(f.Values)]
	f.next++
	return v
}
