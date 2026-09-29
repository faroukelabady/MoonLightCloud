package devicecontrol

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

func isConflict(err error) bool {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Kind == apperr.Conflict
	}
	return false
}

// Service orchestrates Sync Now creation and device-side transitions.
// All timestamps use Cloud server time; Retail clocks are never trusted.
type Service struct {
	store   Store
	devices DeviceStatus
	newID   func() string
	lease   time.Duration
	now     func() time.Time
}

func NewService(store Store, devices DeviceStatus, newID func() string, lease time.Duration, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, devices: devices, newID: newID, lease: lease, now: now}
}

// CreateSyncRequest implements dashboard Sync Now: idempotent on
// (device_id, idempotency_key), one active command per device.
func (s *Service) CreateSyncRequest(ctx context.Context, deviceID, idempotencyKey string) (Command, bool, error) {
	if err := ValidateIdempotencyKey(idempotencyKey); err != nil {
		return Command{}, false, err
	}
	active, err := s.devices.IsActive(ctx, deviceID)
	if err != nil {
		return Command{}, false, err
	}
	if !active {
		return Command{}, false, apperr.New(apperr.InvalidInput, "DEVICE_NOT_ACTIVE")
	}
	if existing, ok, err := s.store.GetByIdempotency(ctx, deviceID, idempotencyKey); err != nil {
		return Command{}, false, err
	} else if ok {
		if existing.Type != CommandSyncNow || existing.Version != CommandVersionV1 {
			return Command{}, false, apperr.New(apperr.Conflict, "DEVICE_COMMAND_CONFLICT")
		}
		return existing, false, nil
	}
	if activeCmd, ok, err := s.store.GetActive(ctx, deviceID); err != nil {
		return Command{}, false, err
	} else if ok {
		return activeCmd, false, apperr.New(apperr.Conflict, "DEVICE_SYNC_ALREADY_ACTIVE")
	}
	now := s.now().UTC()
	cmd, err := s.store.CreateCommand(ctx, s.newID(), deviceID, idempotencyKey, now)
	if err != nil {
		if isConflict(err) {
			// Concurrent creator won: converge deterministically.
			if byKey, ok, qerr := s.store.GetByIdempotency(ctx, deviceID, idempotencyKey); qerr == nil && ok {
				if byKey.Type != CommandSyncNow || byKey.Version != CommandVersionV1 {
					return Command{}, false, apperr.New(apperr.Conflict, "DEVICE_COMMAND_CONFLICT")
				}
				return byKey, false, nil
			}
			if activeCmd, ok, qerr := s.store.GetActive(ctx, deviceID); qerr == nil && ok {
				return activeCmd, false, apperr.New(apperr.Conflict, "DEVICE_SYNC_ALREADY_ACTIVE")
			}
		}
		return Command{}, false, err
	}
	return cmd, true, nil
}

// Poll claims at most one due command for the authenticated device.
func (s *Service) Poll(ctx context.Context, deviceID string) (Command, bool, error) {
	now := s.now().UTC()
	if err := s.store.TouchSeen(ctx, deviceID, now, true); err != nil {
		return Command{}, false, err
	}
	return s.store.PollClaim(ctx, deviceID, now, s.lease)
}

// Accept records durable Retail receipt; idempotent past terminal.
func (s *Service) Accept(ctx context.Context, deviceID, commandID string) (Command, error) {
	now := s.now().UTC()
	cmd, ok, err := s.store.GetCommandForDevice(ctx, commandID, deviceID)
	if err != nil {
		return Command{}, err
	}
	if !ok {
		return Command{}, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND")
	}
	if IsTerminal(cmd.Status) {
		// Late accepted after terminal: no-op on the command, but the
		// authorized contact is fresh presence evidence.
		_ = s.store.TouchSeen(ctx, deviceID, s.now().UTC(), false)
		return cmd, nil
	}
	updated, ok, err := s.store.Accept(ctx, commandID, deviceID, now)
	if err != nil {
		return Command{}, err
	}
	if !ok {
		return s.storeGet(ctx, commandID, deviceID)
	}
	_ = s.store.TouchAccepted(ctx, deviceID, now)
	_ = s.store.TouchSeen(ctx, deviceID, now, false)
	return updated, nil
}

// ReportRunning is informational but durable; never regresses terminal.
func (s *Service) ReportRunning(ctx context.Context, deviceID, commandID string) (Command, error) {
	now := s.now().UTC()
	cmd, ok, err := s.store.GetCommandForDevice(ctx, commandID, deviceID)
	if err != nil {
		return Command{}, err
	}
	if !ok {
		return Command{}, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND")
	}
	if IsTerminal(cmd.Status) {
		// Late running after terminal: no-op on the command, but the
		// authorized contact is fresh presence evidence.
		_ = s.store.TouchSeen(ctx, deviceID, s.now().UTC(), false)
		return cmd, nil
	}
	updated, ok, err := s.store.MarkRunning(ctx, commandID, deviceID, now)
	if err != nil {
		return Command{}, err
	}
	if !ok {
		return s.storeGet(ctx, commandID, deviceID)
	}
	_ = s.store.TouchSeen(ctx, deviceID, now, false)
	return updated, nil
}

// ReportTerminal commits completed/failed; first terminal wins.
func (s *Service) ReportTerminal(ctx context.Context, deviceID, commandID, status, resultCode string) (Command, error) {
	if status != StatusCompleted && status != StatusFailed {
		return Command{}, apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_TRANSITION")
	}
	if err := ValidateResultCode(resultCode); err != nil {
		return Command{}, err
	}
	if (status == StatusCompleted && resultCode != "SYNC_COMPLETED") ||
		(status == StatusFailed && resultCode == "SYNC_COMPLETED") {
		return Command{}, apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_TRANSITION")
	}
	now := s.now().UTC()
	cmd, ok, err := s.store.GetCommandForDevice(ctx, commandID, deviceID)
	if err != nil {
		return Command{}, err
	}
	if !ok {
		return Command{}, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND")
	}
	if IsTerminal(cmd.Status) {
		if cmd.Status != status {
			return Command{}, apperr.New(apperr.Conflict, "DEVICE_COMMAND_CONFLICT")
		}
		// Valid replay: outcome immutable, but the authorized contact is
		// fresh — refresh presence from server time.
		_ = s.store.TouchFinished(ctx, deviceID, s.now().UTC())
		_ = s.store.TouchSeen(ctx, deviceID, s.now().UTC(), false)
		return cmd, nil
	}
	updated, ok, err := s.store.Finish(ctx, commandID, deviceID, status, resultCode, now)
	if err != nil {
		return Command{}, err
	}
	if !ok {
		// Lost a concurrent terminal race: apply the same replay rules
		// against authoritative durable state. First terminal wins;
		// contradiction conflicts; identical replay succeeds.
		current, cerr := s.storeGet(ctx, commandID, deviceID)
		if cerr != nil {
			return Command{}, cerr
		}
		if IsTerminal(current.Status) {
			if current.Status != status {
				return Command{}, apperr.New(apperr.Conflict, "DEVICE_COMMAND_CONFLICT")
			}
			_ = s.store.TouchFinished(ctx, deviceID, s.now().UTC())
			_ = s.store.TouchSeen(ctx, deviceID, s.now().UTC(), false)
			return current, nil
		}
		// State moved non-terminally under us (e.g. accept landed first):
		// retry once against the fresh state.
		retry, rok, rerr := s.store.Finish(ctx, commandID, deviceID, status, resultCode, s.now().UTC())
		if rerr != nil {
			return Command{}, rerr
		}
		if !rok {
			current, cerr := s.storeGet(ctx, commandID, deviceID)
			if cerr != nil {
				return Command{}, cerr
			}
			if IsTerminal(current.Status) && current.Status != status {
				return Command{}, apperr.New(apperr.Conflict, "DEVICE_COMMAND_CONFLICT")
			}
			return current, nil
		}
		_ = s.store.TouchFinished(ctx, deviceID, s.now().UTC())
		_ = s.store.TouchSeen(ctx, deviceID, s.now().UTC(), false)
		return retry, nil
	}
	_ = s.store.TouchFinished(ctx, deviceID, now)
	_ = s.store.TouchSeen(ctx, deviceID, now, false)
	return updated, nil
}

// Presence loads server-observed contact for dashboards.
func (s *Service) Presence(ctx context.Context, deviceID string) (Presence, bool, error) {
	return s.store.GetPresence(ctx, deviceID)
}

// Active returns the non-terminal command, if any.
func (s *Service) Active(ctx context.Context, deviceID string) (Command, bool, error) {
	return s.store.GetActive(ctx, deviceID)
}

// Recent returns bounded history.
func (s *Service) Recent(ctx context.Context, deviceID string, limit int) ([]Command, error) {
	return s.store.Recent(ctx, deviceID, limit)
}

func (s *Service) storeGet(ctx context.Context, id, deviceID string) (Command, error) {
	cmd, ok, err := s.store.GetCommandForDevice(ctx, id, deviceID)
	if err != nil {
		return Command{}, err
	}
	if !ok {
		return Command{}, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND")
	}
	return cmd, nil
}

// DeviceOverview aggregates one device's control-plane read model.
type DeviceOverview struct {
	Presence *Presence
	Active   *Command
	Recent   []Command
}

// Overview serves dashboard listings with a fixed number of batched reads
// (presence, active commands, bounded history) regardless of device count.
// No credentials are involved at any step.
func (s *Service) Overview(ctx context.Context, deviceIDs []string) (map[string]DeviceOverview, error) {
	out := make(map[string]DeviceOverview, len(deviceIDs))
	for _, id := range deviceIDs {
		out[id] = DeviceOverview{}
	}
	presences, err := s.store.ListPresence(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range presences {
		if ov, ok := out[p.DeviceID]; ok {
			p := p
			ov.Presence = &p
			out[p.DeviceID] = ov
		}
	}
	actives, err := s.store.ListActiveAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range actives {
		if ov, ok := out[c.DeviceID]; ok {
			c := c
			ov.Active = &c
			out[c.DeviceID] = ov
		}
	}
	recent, err := s.store.ListRecentBounded(ctx, deviceIDs, 5)
	if err != nil {
		return nil, err
	}
	for _, c := range recent {
		if ov, ok := out[c.DeviceID]; ok {
			ov.Recent = append(ov.Recent, c)
			out[c.DeviceID] = ov
		}
	}
	return out, nil
}
