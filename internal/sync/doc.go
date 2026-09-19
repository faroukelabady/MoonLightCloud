// Package sync reserves the future desktop/cloud sync protocol.
// Phase 1A implements nothing here. Principles (offline-first desktop,
// transactional local outbox, idempotent cloud ingestion, at-least-once
// delivery, server deduplication, versioned payloads) are documented in
// docs/sync/protocol.md. There is intentionally no shared domain package
// with MoonLightRetail: the versioned HTTPS contract is the boundary.
package sync
