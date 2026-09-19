// Package notifications reserves the future NotificationProvider boundary.
// Phase 1A implements nothing here. See ADR-0008.
package notifications

import "context"

// Message is a placeholder sketch of the direction, not a contract.
type Message struct {
	To   string
	Body string
}

// Provider is the future seam for Meta WhatsApp, Twilio, email, and others.
type Provider interface {
	Name() string
	// Future: Send(ctx, msg) with idempotency keys...
	Close(ctx context.Context) error
}
