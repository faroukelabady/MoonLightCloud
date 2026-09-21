// Package sync defines the versioned desktop/cloud ingestion protocol and
// its server-side implementation. Transport + durable acceptance only:
// no business projection happens here (Phase 1B excludes sale/product/
// inventory processing). The contract in docs/sync/protocol.md is what
// MoonLightRetail's future outbox implements against.
package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

// Protocol limits (conservative; sales/returns/product/inventory shaped,
// never media transfer). Mirrored in capabilities + OpenAPI + docs.
const (
	APIVersion       = "v1"
	MaxBatchEvents   = 100
	MaxPayloadBytes  = 256 * 1024
	MaxSyncBodyBytes = 8 * 1024 * 1024
)

// Per-event acceptance statuses. Batches never mix partial commits:
// malformed/conflicting batches fail the whole request with an error.
const (
	StatusAccepted        = "accepted"
	StatusAlreadyAccepted = "already_accepted"
)

// SupportedEvents is the registry of ingestible event types. Unknown-
// but-well-formed types are rejected (422) so clients fail fast instead
// of assuming durability. Entries are added at startup once their
// validator, projection schema, projector, and migrations exist.
var SupportedEvents = map[string]bool{
	"system.test.v1": true,
}

// RegisterEventType advertises a business event with its payload validator.
// The validator runs before durable ACK (invalid payloads reject the whole
// batch); the projector consumes accepted events asynchronously.
func RegisterEventType(eventType string, validate func(json.RawMessage) error) {
	SupportedEvents[eventType] = true
	validators[eventType] = validate
}

var validators = map[string]func(json.RawMessage) error{}

var eventTypeFormat = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*\.v[0-9]+$`)

// Clock-skew policy: occurred_at is business metadata, never authority.
// Bounds reject only absurd values: before 2020-01-01 or more than 24h in
// the future (generous for wrong desktop clocks; auth/dedup never use it).
var (
	minOccurredAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	maxFutureSkew = 24 * time.Hour
)

// Canonical payload-hash versions. v1 marks rows stored with the pre-2D
// float64 canonicalizer; v2 is the exact decimal canonicalizer used for all
// new events. v1 hashes are version metadata only — never proof of payload
// equality (duplicate verification re-canonicalizes the stored immutable
// payload with the exact canonicalizer, fail-closed).
const (
	HashVersionLegacy = 1
	HashVersionExact  = 2
)

// Event is one validated sync event ready for durable ingestion.
type Event struct {
	EventID    string
	EventType  string
	DeviceID   string // always the authenticated device, never trusted input
	OccurredAt time.Time
	Payload    json.RawMessage // exact canonical form (v2)
	Hash       []byte          // SHA-256 over exact canonical payload
}

// Batch is a validated ingestion batch.
type Batch struct {
	BatchID string // optional client correlation, echoed back
	Events  []Event
}

// EventResult is per-event durable outcome.
type EventResult struct {
	EventID string `json:"event_id"`
	Status  string `json:"status"`
}

// BatchResult is the success response body.
type BatchResult struct {
	BatchID string        `json:"batch_id,omitempty"`
	Events  []EventResult `json:"events"`
}

// Repository persists batches atomically behind the postgres adapter.
type Repository interface {
	IngestBatch(ctx context.Context, deviceID, credentialID string, events []Event, receivedAt time.Time) ([]EventResult, error)
}

// Service validates and ingests sync batches.
type Service struct {
	repo  Repository
	clock clock.Clock
}

// NewService wires the sync service.
func NewService(r Repository, c clock.Clock) Service { return Service{repo: r, clock: c} }

// Capabilities describes server sync support for future desktop compat.
type Capabilities struct {
	APIVersion      string   `json:"api_version"`
	SupportedEvents []string `json:"supported_events"`
	MaxBatchEvents  int      `json:"max_batch_events"`
	MaxPayloadBytes int      `json:"max_payload_bytes"`
	MaxBodyBytes    int      `json:"max_body_bytes"`
	ServerTime      string   `json:"server_time"`
}

// Capabilities builds the capabilities response.
func (s Service) Capabilities() Capabilities {
	events := make([]string, 0, len(SupportedEvents))
	for e := range SupportedEvents {
		events = append(events, e)
	}
	return Capabilities{
		APIVersion: APIVersion, SupportedEvents: events,
		MaxBatchEvents: MaxBatchEvents, MaxPayloadBytes: MaxPayloadBytes,
		MaxBodyBytes: MaxSyncBodyBytes, ServerTime: s.clock.Now().Format(time.RFC3339Nano),
	}
}

// Ingest validates a raw batch body for an authenticated device and durably
// commits it. Success means PostgreSQL commit; duplicates return
// already_accepted; any malformed/conflicting batch fails whole (no partial
// commit) so client retry stays straightforward.
func (s Service) Ingest(ctx context.Context, deviceID, credentialID string, body []byte) (BatchResult, error) {
	b, err := ParseBatch(body, deviceID, s.clock.Now())
	if err != nil {
		return BatchResult{}, err
	}
	// Event-specific validation before durable ACK: a structurally invalid
	// business payload rejects the entire batch; nothing commits.
	for i := range b.Events {
		if validate, ok := validators[b.Events[i].EventType]; ok {
			if err := validate(b.Events[i].Payload); err != nil {
				return BatchResult{}, fmt.Errorf("event %d: %w", i, err)
			}
		}
	}
	receivedAt := s.clock.Now()
	results, err := s.repo.IngestBatch(ctx, deviceID, credentialID, b.Events, receivedAt)
	if err != nil {
		return BatchResult{}, err
	}
	return BatchResult{BatchID: b.BatchID, Events: results}, nil
}

// rawBatch mirrors the wire shape; device_id is optional client echo.
type rawBatch struct {
	BatchID *string           `json:"batch_id"`
	Events  []json.RawMessage `json:"events"`
}

type rawEvent struct {
	EventID   *string         `json:"event_id"`
	EventType *string         `json:"event_type"`
	DeviceID  *string         `json:"device_id"`
	Occurred  *string         `json:"occurred_at"`
	Payload   json.RawMessage `json:"payload"`
}

// ParseBatch validates the wire batch for the authenticated device.
// Unknown additive envelope fields are tolerated for forward compatibility;
// duplicate JSON keys are rejected as client construction errors.
func ParseBatch(body []byte, authedDeviceID string, now time.Time) (Batch, error) {
	if err := rejectDuplicateKeys(body); err != nil {
		return Batch{}, apperr.New(apperr.InvalidInput, "malformed batch: duplicate JSON keys")
	}
	var rb rawBatch
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&rb); err != nil {
		return Batch{}, apperr.New(apperr.InvalidInput, "malformed batch: invalid JSON")
	}
	if dec.More() {
		return Batch{}, apperr.New(apperr.InvalidInput, "malformed batch: trailing data")
	}
	var out Batch
	if rb.BatchID != nil {
		if !isUUID(*rb.BatchID) {
			return Batch{}, apperr.New(apperr.InvalidInput, "malformed batch: batch_id must be a UUID")
		}
		out.BatchID = *rb.BatchID
	}
	if len(rb.Events) == 0 {
		return Batch{}, apperr.New(apperr.InvalidInput, "malformed batch: events must not be empty")
	}
	if len(rb.Events) > MaxBatchEvents {
		return Batch{}, apperr.New(apperr.TooLarge,
			fmt.Sprintf("batch too large: %d events, max %d", len(rb.Events), MaxBatchEvents))
	}
	seen := make(map[string]bool, len(rb.Events))
	for i, raw := range rb.Events {
		ev, err := parseEvent(raw, authedDeviceID, now)
		if err != nil {
			return Batch{}, fmt.Errorf("event %d: %w", i, err)
		}
		if seen[ev.EventID] {
			return Batch{}, apperr.New(apperr.InvalidInput,
				fmt.Sprintf("malformed batch: duplicate event_id %s inside batch", ev.EventID))
		}
		seen[ev.EventID] = true
		out.Events = append(out.Events, ev)
	}
	return out, nil
}

func parseEvent(raw json.RawMessage, authedDeviceID string, now time.Time) (Event, error) {
	if err := rejectDuplicateKeys(raw); err != nil {
		return Event{}, apperr.New(apperr.InvalidInput, "malformed event: duplicate JSON keys")
	}
	var re rawEvent
	if err := json.Unmarshal(raw, &re); err != nil {
		return Event{}, apperr.New(apperr.InvalidInput, "malformed event: invalid JSON")
	}
	if re.EventID == nil || !isUUID(*re.EventID) {
		return Event{}, apperr.New(apperr.InvalidInput, "malformed event: event_id must be a UUID")
	}
	if re.EventType == nil || !eventTypeFormat.MatchString(*re.EventType) {
		return Event{}, apperr.New(apperr.InvalidInput, "malformed event: invalid event_type")
	}
	if !SupportedEvents[*re.EventType] {
		return Event{}, apperr.New(apperr.Unprocessable,
			fmt.Sprintf("unsupported event type %q", *re.EventType))
	}
	// Device identity comes from authentication. An echoed device_id must
	// match; anything else is impersonation.
	if re.DeviceID != nil && *re.DeviceID != authedDeviceID {
		return Event{}, apperr.New(apperr.Forbidden, "event device_id does not match authenticated device")
	}
	if re.Occurred == nil {
		return Event{}, apperr.New(apperr.InvalidInput, "malformed event: occurred_at is required")
	}
	occurred, err := time.Parse(time.RFC3339Nano, *re.Occurred)
	if err != nil {
		if occurred, err = time.Parse(time.RFC3339, *re.Occurred); err != nil {
			return Event{}, apperr.New(apperr.InvalidInput, "malformed event: occurred_at must be RFC3339")
		}
	}
	occurred = occurred.UTC()
	// Envelope instants have microsecond resolution (PostgreSQL TIMESTAMPTZ):
	// sub-microsecond digits are normalized at ingest so retries of the same
	// event compare equal after the storage round-trip. Business ordering
	// never depends on sub-microsecond precision.
	occurred = occurred.Truncate(time.Microsecond)
	if occurred.Before(minOccurredAt) || occurred.After(now.Add(maxFutureSkew)) {
		return Event{}, apperr.New(apperr.Unprocessable, "event occurred_at outside acceptable bounds")
	}
	if len(re.Payload) == 0 {
		return Event{}, apperr.New(apperr.InvalidInput, "malformed event: payload is required")
	}
	if len(re.Payload) > MaxPayloadBytes {
		return Event{}, apperr.New(apperr.TooLarge,
			fmt.Sprintf("event payload too large: %d bytes, max %d", len(re.Payload), MaxPayloadBytes))
	}
	canonical, err := canonicalJSON(re.Payload)
	if err != nil {
		return Event{}, apperr.New(apperr.InvalidInput, "malformed event: payload must be a JSON object")
	}
	sum := sha256.Sum256(canonical)
	return Event{
		EventID: *re.EventID, EventType: *re.EventType,
		DeviceID: authedDeviceID, OccurredAt: occurred,
		Payload: canonical, Hash: sum[:],
	}, nil
}

// canonicalJSON re-marshals parsed JSON so semantically identical payloads
// hash identically regardless of field order or insignificant whitespace.
//
// Number semantics (rule A, exact): numerically equivalent JSON spellings
// (1, 1.0, 1e0, 10e-1) canonicalize identically via bounded exact decimal
// normalization in canonical.go. No number passes through float64; integer
// values (including MaxInt64 and 2^53+1) are preserved exactly. Hostile
// exponents that would force enormous expansions are rejected as malformed
// before ACK. Payloads must be JSON objects.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	norm, err := normalizeCanonicalValue(obj)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(norm)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Canonicalize re-canonicalizes stored JSON bytes with the exact v2
// canonicalizer. Used to verify duplicate retries against the immutable
// stored payload (fail-closed); never for new-event hashing outside ingest.
func Canonicalize(raw json.RawMessage) (json.RawMessage, error) {
	return canonicalJSON(raw)
}

// rejectDuplicateKeys walks the JSON token stream and rejects objects with
// a repeated key at any level (obvious client construction error).
func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk, walkValue func() error
	walkValue = func() error { return walk() }
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := kt.(string)
				if !ok {
					return fmt.Errorf("bad key")
				}
				if seen[key] {
					return fmt.Errorf("duplicate key %q", key)
				}
				seen[key] = true
				if err := walkValue(); err != nil {
					return err
				}
			}
			_, err = dec.Token() // closing }
			return err
		case '[':
			for dec.More() {
				if err := walkValue(); err != nil {
					return err
				}
			}
			_, err = dec.Token() // closing ]
			return err
		}
		return nil
	}
	// Top level must be a single value; walk handles object/array/scalar.
	if err := walk(); err != nil {
		return err
	}
	return nil
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
