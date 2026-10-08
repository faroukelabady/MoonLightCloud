package fleetupdate

import (
	"context"
	"errors"

	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/release"
)

// ReleaseView is the operator view of a release. Identity fields come
// only from the verified signed manifest, never from operator text.
type ReleaseView struct {
	ID                   string         `json:"id"`
	ManifestDigest       string         `json:"manifest_digest"`
	ReleaseSequence      int64          `json:"release_sequence"`
	Version              string         `json:"version"`
	BuildCommit          string         `json:"build_commit"`
	MinInstalledSequence int64          `json:"min_installed_sequence"`
	KeyID                string         `json:"key_id"`
	Status               string         `json:"status"`
	ImportedBy           string         `json:"imported_by"`
	ImportedAt           time.Time      `json:"imported_at"`
	StatusChangedBy      string         `json:"status_changed_by"`
	StatusChangedAt      time.Time      `json:"status_changed_at"`
	Artifacts            []ArtifactView `json:"artifacts"`
}

// ArtifactView is one signed artifact plus its operational location.
type ArtifactView struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Package  string `json:"package"`
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	URL      string `json:"url"`
}

// RolloutView is a rollout plus per-state target counts.
type RolloutView struct {
	ID             string           `json:"id"`
	ReleaseID      string           `json:"release_id"`
	ReleaseVersion string           `json:"release_version"`
	ReleaseSeq     int64            `json:"release_sequence"`
	Scope          string           `json:"scope"`
	StoreID        string           `json:"store_id,omitempty"`
	DeviceID       string           `json:"device_id,omitempty"`
	Mode           string           `json:"mode"`
	Percentage     int              `json:"percentage"`
	Status         string           `json:"status"`
	NotBefore      *time.Time       `json:"not_before,omitempty"`
	TargetCount    int              `json:"target_count"`
	CreatedBy      string           `json:"created_by"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	Counts         map[string]int64 `json:"counts,omitempty"`
}

// TargetView is one device target of a rollout.
type TargetView struct {
	ID           string     `json:"id"`
	RolloutID    string     `json:"rollout_id"`
	DeviceID     string     `json:"device_id"`
	DeviceName   string     `json:"device_name"`
	StoreID      string     `json:"store_id"`
	State        string     `json:"state"`
	AttemptCount int        `json:"attempt_count"`
	NextAttempt  *time.Time `json:"next_attempt_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TargetEvent is one append-only history row.
type TargetEvent struct {
	ReportedState string    `json:"reported_state"`
	TargetState   string    `json:"target_state"`
	ErrorCode     string    `json:"error_code,omitempty"`
	Retryable     bool      `json:"retryable"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// FleetRow is one device with its trusted version report.
type FleetRow struct {
	DeviceID          string     `json:"device_id"`
	DeviceName        string     `json:"device_name"`
	DeviceStatus      string     `json:"device_status"`
	StoreID           string     `json:"store_id"`
	LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
	Version           string     `json:"version,omitempty"`
	BuildCommit       string     `json:"build_commit,omitempty"`
	ReleaseSequence   int64      `json:"release_sequence"`
	OS                string     `json:"os,omitempty"`
	Arch              string     `json:"arch,omitempty"`
	UpdaterProtocol   int        `json:"updater_protocol"`
	UpdaterCapable    bool       `json:"updater_capable"`
	UpdaterReported   bool       `json:"updater_reported"`
	UnsupportedReason string     `json:"unsupported_reason,omitempty"`
	UpdateState       string     `json:"update_state,omitempty"`
	UpdateError       string     `json:"update_error,omitempty"`
	ReportedAt        *time.Time `json:"reported_at,omitempty"`
	TargetID          string     `json:"target_id,omitempty"`
	TargetState       string     `json:"target_state,omitempty"`
	TargetError       string     `json:"target_error,omitempty"`
	TargetVersion     string     `json:"target_version,omitempty"`
	TargetSequence    int64      `json:"target_sequence,omitempty"`
	RolloutID         string     `json:"rollout_id,omitempty"`
}

// AuditEvent is one audit row.
type AuditEvent struct {
	ID         int64          `json:"id"`
	OccurredAt time.Time      `json:"occurred_at"`
	ActorKind  string         `json:"actor_kind"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	ReleaseID  string         `json:"release_id,omitempty"`
	RolloutID  string         `json:"rollout_id,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	DeviceID   string         `json:"device_id,omitempty"`
	StoreID    string         `json:"store_id,omitempty"`
	Details    map[string]any `json:"details"`
}

// StatusReport is the authenticated device version/updater report.
type StatusReport struct {
	Version           string `json:"version"`
	BuildCommit       string `json:"build_commit"`
	ReleaseSequence   int64  `json:"release_sequence"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	UpdaterProtocol   int    `json:"updater_protocol"`
	UpdaterCapable    bool   `json:"updater_capable"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
	UpdateState       string `json:"update_state"`
	UpdateError       string `json:"update_error,omitempty"`
	TargetID          string `json:"target_id,omitempty"`
}

// TargetReport is the authenticated per-target progress report.
type TargetReport struct {
	State           string `json:"state"`
	ErrorCode       string `json:"error_code,omitempty"`
	Retryable       bool   `json:"retryable"`
	ReleaseSequence int64  `json:"release_sequence"`
	ManifestDigest  string `json:"manifest_digest"`
}

// Command is the narrow install intent delivered to a device.
type Command struct {
	TargetID       string     `json:"target_id"`
	Type           string     `json:"type"`
	Version        int        `json:"version"`
	ReleaseID      string     `json:"release_id"`
	ManifestDigest string     `json:"manifest_digest"`
	Mode           string     `json:"mode"`
	NotBefore      *time.Time `json:"not_before"`
	Envelope       string     `json:"envelope"`
	ArtifactURL    string     `json:"artifact_url"`
	ReleaseStatus  string     `json:"release_status"`
}

// Ack is Cloud's view returned to a reporting device.
type Ack struct {
	TargetState   string `json:"target_state"`
	ReleaseStatus string `json:"release_status"`
}

// ReleaseImport is a verified release ready to persist.
type ReleaseImport struct {
	ID        string
	Verified  release.Verified
	Envelope  string
	Artifacts []ArtifactView
}

// Store is the transactional persistence port (PostgreSQL adapter).
type Store interface {
	ImportRelease(ctx context.Context, rec ReleaseImport, actor string, now time.Time) (ReleaseView, bool, error)
	GetRelease(ctx context.Context, id string) (ReleaseView, error)
	ListReleases(ctx context.Context, beforeSequence int64, limit int) ([]ReleaseView, error)
	SetReleaseStatus(ctx context.Context, id, status, actor string, now time.Time) (ReleaseView, error)
	CreateRollout(ctx context.Context, id string, req RolloutRequest, actor string, newID func() string, now time.Time) (RolloutView, error)
	TransitionRollout(ctx context.Context, id string, from []string, to, action, actor string, now time.Time) (RolloutView, error)
	SetRolloutPercentage(ctx context.Context, id string, percentage int, actor string, now time.Time) (RolloutView, error)
	GetRollout(ctx context.Context, id string) (RolloutView, error)
	ListRollouts(ctx context.Context, storeID string, cursorAt *time.Time, cursorID string, limit int) ([]RolloutView, error)
	ListTargets(ctx context.Context, rolloutID, storeID, afterDevice string, limit int) ([]TargetView, error)
	TargetHistory(ctx context.Context, targetID string, limit int) ([]TargetEvent, error)
	Fleet(ctx context.Context, storeID, afterDevice string, limit int) ([]FleetRow, error)
	Audit(ctx context.Context, storeID string, beforeID int64, limit int) ([]AuditEvent, error)
	RecordStatus(ctx context.Context, deviceID string, s StatusReport, now time.Time) error
	ClaimCommand(ctx context.Context, deviceID string, now time.Time) (*Command, error)
	ApplyReport(ctx context.Context, deviceID, targetID string, r TargetReport, now time.Time) (Ack, error)
}

// Config tunes the service.
type Config struct {
	// AllowHTTPArtifacts permits http:// artifact locations (development
	// only). Production requires https; integrity never depends on it.
	AllowHTTPArtifacts bool
}

// Service orchestrates validation, the single application clock and
// identifiers over the Store.
type Service struct {
	store    Store
	trust    release.TrustSet
	trustErr error
	now      func() time.Time
	newID    func() string
	cfg      Config
}

// NewService wires the fleet service. A Cloud without trusted release
// keys cannot import releases (fail closed), but keeps serving devices.
func NewService(store Store, trust release.TrustSet, trustErr error, now func() time.Time, newID func() string, cfg Config) *Service {
	return &Service{store: store, trust: trust, trustErr: trustErr, now: now, newID: newID, cfg: cfg}
}

func (s *Service) clock() time.Time { return s.now().UTC() }

var actorPattern = regexp.MustCompile(`^[^\x00-\x1f]{1,128}$`)

func validActor(actor string) bool { return actorPattern.MatchString(actor) }

// ImportRelease verifies a signed envelope and registers it immutably.
// artifactURLs maps signed artifact file names to operational locations;
// every signed artifact must have exactly one location.
func (s *Service) ImportRelease(ctx context.Context, actor string, envelope []byte, artifactURLs map[string]string) (ReleaseView, bool, error) {
	if !validActor(actor) {
		return ReleaseView{}, false, apperr.New(apperr.InvalidInput, "invalid actor")
	}
	if s.trustErr != nil || s.trust.Len() == 0 {
		return ReleaseView{}, false, apperr.New(apperr.Conflict, "RELEASE_TRUST_NOT_CONFIGURED")
	}
	verified, err := s.trust.Verify(envelope)
	if err != nil {
		return ReleaseView{}, false, apperr.New(apperr.InvalidInput, string(release.CodeOf(err)))
	}
	if len(artifactURLs) != len(verified.Manifest.Artifacts) {
		return ReleaseView{}, false, apperr.New(apperr.InvalidInput, "every signed artifact needs exactly one location")
	}
	artifacts := make([]ArtifactView, 0, len(verified.Manifest.Artifacts))
	for _, a := range verified.Manifest.Artifacts {
		location, ok := artifactURLs[a.FileName]
		if !ok || !s.validArtifactURL(location) {
			return ReleaseView{}, false, apperr.New(apperr.InvalidInput, "artifact location missing or not https: "+a.FileName)
		}
		artifacts = append(artifacts, ArtifactView{OS: a.OS, Arch: a.Arch, Package: a.Package, FileName: a.FileName,
			Size: a.Size, SHA256: a.SHA256, URL: location})
	}
	rec := ReleaseImport{ID: s.newID(), Verified: verified, Envelope: string(envelope), Artifacts: artifacts}
	return s.store.ImportRelease(ctx, rec, actor, s.clock())
}

func (s *Service) validArtifactURL(raw string) bool {
	if len(raw) > 2048 || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return u.Scheme == "https" || (s.cfg.AllowHTTPArtifacts && u.Scheme == "http")
}

// SetReleaseStatus activates or revokes a release. Revocation stops NEW
// installs; it never uninstalls a running Retail (prompt §46).
func (s *Service) SetReleaseStatus(ctx context.Context, actor, id, status string) (ReleaseView, error) {
	if !validActor(actor) || !ValidUUID(id) || (status != ReleaseActive && status != ReleaseRevoked) {
		return ReleaseView{}, apperr.New(apperr.InvalidInput, "invalid release status change")
	}
	return s.store.SetReleaseStatus(ctx, id, status, actor, s.clock())
}

// GetRelease returns one release.
func (s *Service) GetRelease(ctx context.Context, id string) (ReleaseView, error) {
	if !ValidUUID(id) {
		return ReleaseView{}, apperr.New(apperr.InvalidInput, "invalid release id")
	}
	return s.store.GetRelease(ctx, id)
}

// ListReleases returns a bounded page, newest sequence first.
func (s *Service) ListReleases(ctx context.Context, beforeSequence int64, limit int) ([]ReleaseView, error) {
	return s.store.ListReleases(ctx, beforeSequence, clampLimit(limit))
}

func clampLimit(limit int) int {
	if limit < 1 || limit > 100 {
		return 50
	}
	return limit
}

// CreateRollout snapshots eligible targets as a DRAFT rollout.
func (s *Service) CreateRollout(ctx context.Context, actor string, req RolloutRequest) (RolloutView, error) {
	if !validActor(actor) {
		return RolloutView{}, apperr.New(apperr.InvalidInput, "invalid actor")
	}
	if err := req.Validate(); err != nil {
		return RolloutView{}, apperr.New(apperr.InvalidInput, "invalid rollout request")
	}
	if req.NotBefore != nil {
		nb := req.NotBefore.UTC()
		req.NotBefore = &nb
	}
	return s.store.CreateRollout(ctx, s.newID(), req, actor, s.newID, s.clock())
}

// Rollout lifecycle actions.
func (s *Service) StartRollout(ctx context.Context, actor, id string) (RolloutView, error) {
	return s.transition(ctx, actor, id, []string{RolloutDraft}, RolloutActive, "rollout.started")
}

func (s *Service) PauseRollout(ctx context.Context, actor, id string) (RolloutView, error) {
	return s.transition(ctx, actor, id, []string{RolloutActive}, RolloutPaused, "rollout.paused")
}

func (s *Service) ResumeRollout(ctx context.Context, actor, id string) (RolloutView, error) {
	return s.transition(ctx, actor, id, []string{RolloutPaused}, RolloutActive, "rollout.resumed")
}

// CancelRollout cancels targets not yet in irreversible execution.
func (s *Service) CancelRollout(ctx context.Context, actor, id string) (RolloutView, error) {
	return s.transition(ctx, actor, id, []string{RolloutDraft, RolloutActive, RolloutPaused}, RolloutCancelled, "rollout.cancelled")
}

func (s *Service) transition(ctx context.Context, actor, id string, from []string, to, action string) (RolloutView, error) {
	if !validActor(actor) || !ValidUUID(id) {
		return RolloutView{}, apperr.New(apperr.InvalidInput, "invalid rollout")
	}
	return s.store.TransitionRollout(ctx, id, from, to, action, actor, s.clock())
}

// IncreasePercentage widens a staged rollout deterministically.
func (s *Service) IncreasePercentage(ctx context.Context, actor, id string, percentage int) (RolloutView, error) {
	if !validActor(actor) || !ValidUUID(id) || percentage < 1 || percentage > 100 {
		return RolloutView{}, apperr.New(apperr.InvalidInput, "invalid percentage")
	}
	return s.store.SetRolloutPercentage(ctx, id, percentage, actor, s.clock())
}

// GetRollout returns one rollout with counts.
func (s *Service) GetRollout(ctx context.Context, id string) (RolloutView, error) {
	if !ValidUUID(id) {
		return RolloutView{}, apperr.New(apperr.InvalidInput, "invalid rollout id")
	}
	return s.store.GetRollout(ctx, id)
}

// ListRollouts returns a keyset page (cursor bound to the Store filter).
func (s *Service) ListRollouts(ctx context.Context, storeID string, cursor string, limit int) ([]RolloutView, string, error) {
	if storeID != "" && !ValidUUID(storeID) {
		return nil, "", apperr.New(apperr.InvalidInput, "invalid store id")
	}
	c, err := decodeCursor(cursor, "rollouts", storeID)
	if err != nil {
		return nil, "", err
	}
	var at *time.Time
	if !c.At.IsZero() {
		at = &c.At
	}
	limit = clampLimit(limit)
	rows, err := s.store.ListRollouts(ctx, storeID, at, c.ID, limit)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == limit {
		last := rows[len(rows)-1]
		next = encodeCursor(cursorState{Kind: "rollouts", Scope: storeID, At: last.CreatedAt, ID: last.ID})
	}
	return rows, next, nil
}

// ListTargets returns a rollout's targets (optionally one Store's).
func (s *Service) ListTargets(ctx context.Context, rolloutID, storeID, cursor string, limit int) ([]TargetView, string, error) {
	if !ValidUUID(rolloutID) || (storeID != "" && !ValidUUID(storeID)) {
		return nil, "", apperr.New(apperr.InvalidInput, "invalid filter")
	}
	c, err := decodeCursor(cursor, "targets:"+rolloutID, storeID)
	if err != nil {
		return nil, "", err
	}
	limit = clampLimit(limit)
	rows, err := s.store.ListTargets(ctx, rolloutID, storeID, c.ID, limit)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == limit {
		next = encodeCursor(cursorState{Kind: "targets:" + rolloutID, Scope: storeID, ID: rows[len(rows)-1].DeviceID})
	}
	return rows, next, nil
}

// TargetHistory returns a target's bounded history.
func (s *Service) TargetHistory(ctx context.Context, targetID string) ([]TargetEvent, error) {
	if !ValidUUID(targetID) {
		return nil, apperr.New(apperr.InvalidInput, "invalid target id")
	}
	return s.store.TargetHistory(ctx, targetID, 100)
}

// Fleet returns a keyset page of devices with version/update state.
func (s *Service) Fleet(ctx context.Context, storeID, cursor string, limit int) ([]FleetRow, string, error) {
	if storeID != "" && !ValidUUID(storeID) {
		return nil, "", apperr.New(apperr.InvalidInput, "invalid store id")
	}
	c, err := decodeCursor(cursor, "fleet", storeID)
	if err != nil {
		return nil, "", err
	}
	limit = clampLimit(limit)
	rows, err := s.store.Fleet(ctx, storeID, c.ID, limit)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == limit {
		next = encodeCursor(cursorState{Kind: "fleet", Scope: storeID, ID: rows[len(rows)-1].DeviceID})
	}
	return rows, next, nil
}

// Audit returns a bounded audit page.
func (s *Service) Audit(ctx context.Context, storeID string, beforeID int64, limit int) ([]AuditEvent, error) {
	if storeID != "" && !ValidUUID(storeID) {
		return nil, apperr.New(apperr.InvalidInput, "invalid store id")
	}
	return s.store.Audit(ctx, storeID, beforeID, clampLimit(limit))
}

var (
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	shortPattern   = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	commitPattern  = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)
	statePattern   = regexp.MustCompile(`^[A-Z][A-Z_]{0,63}$`)
)

// RecordStatus stores the authenticated device's version report. The
// device identity comes from authentication, never from the payload.
func (s *Service) RecordStatus(ctx context.Context, deviceID string, r StatusReport) error {
	switch {
	case !ValidUUID(deviceID):
		return apperr.New(apperr.Unauthorized, "device required")
	case !versionPattern.MatchString(r.Version) || !commitPattern.MatchString(r.BuildCommit):
		return apperr.New(apperr.InvalidInput, "invalid version identity")
	case r.ReleaseSequence < 0 || uint64(r.ReleaseSequence) > release.MaxSequence:
		return apperr.New(apperr.InvalidInput, "invalid release sequence")
	case !shortPattern.MatchString(r.OS) || !shortPattern.MatchString(r.Arch):
		return apperr.New(apperr.InvalidInput, "invalid platform")
	case r.UpdaterProtocol < 0 || r.UpdaterProtocol > 100:
		return apperr.New(apperr.InvalidInput, "invalid updater protocol")
	case !statePattern.MatchString(r.UpdateState):
		return apperr.New(apperr.InvalidInput, "invalid update state")
	case r.UnsupportedReason != "" && !ValidCode(r.UnsupportedReason):
		return apperr.New(apperr.InvalidInput, "invalid unsupported reason")
	case r.UpdateError != "" && !ValidCode(r.UpdateError):
		return apperr.New(apperr.InvalidInput, "invalid update error")
	}
	return s.store.RecordStatus(ctx, deviceID, r, s.clock())
}

// PollCommand returns at most one deliverable intent for the device.
func (s *Service) PollCommand(ctx context.Context, deviceID string) (*Command, error) {
	if !ValidUUID(deviceID) {
		return nil, apperr.New(apperr.Unauthorized, "device required")
	}
	return s.store.ClaimCommand(ctx, deviceID, s.clock())
}

// ReportTarget applies one device report to its own target only.
func (s *Service) ReportTarget(ctx context.Context, deviceID, targetID string, r TargetReport) (Ack, error) {
	if !ValidUUID(deviceID) {
		return Ack{}, apperr.New(apperr.Unauthorized, "device required")
	}
	if !ValidUUID(targetID) || !statePattern.MatchString(r.State) || (r.ErrorCode != "" && !ValidCode(r.ErrorCode)) {
		return Ack{}, apperr.New(apperr.InvalidInput, "invalid target report")
	}
	if _, ok := MapReport(r.State, r.ErrorCode); !ok {
		return Ack{}, apperr.New(apperr.InvalidInput, "unknown update state")
	}
	ack, err := s.store.ApplyReport(ctx, deviceID, targetID, r, s.clock())
	if errors.Is(err, ErrNotFound) {
		// Foreign device, foreign Store or unknown target: indistinguishable.
		return Ack{}, apperr.New(apperr.NotFound, "update target not found")
	}
	return ack, err
}
