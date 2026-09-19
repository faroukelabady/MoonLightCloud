# ADR-0008: Notification provider behind an adapter

## Status

Accepted.

## Context

WhatsApp/Twilio/email transports change pricing/APIs; core must not depend on any.

## Decision

internal/notifications.NotificationProvider interface + docs only. No sends in Phase 1A.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
