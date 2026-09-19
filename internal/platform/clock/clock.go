// Package clock abstracts time where deterministic tests need it
// (authentication/expiry logic). Plain time.Now stays elsewhere.
package clock

import "time"

// Clock supplies current UTC time.
type Clock interface {
	Now() time.Time
}

// System is the production clock.
type System struct{}

// Now returns current UTC time.
func (System) Now() time.Time { return time.Now().UTC() }

// Fixed is a deterministic test clock.
type Fixed struct{ T time.Time }

// Now returns the fixed UTC time.
func (f Fixed) Now() time.Time { return f.T.UTC() }
