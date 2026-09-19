# ADR-0007: External commerce behind an adapter

## Status

Accepted.

## Context

Shopify/WooCommerce SDKs and semantics must not leak into the core; providers are replaceable.

## Decision

internal/commerce.CommerceProvider interface + docs only in Phase 1A. No vendor code, no webhooks yet.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
