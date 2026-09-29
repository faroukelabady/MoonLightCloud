package operations

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

// Commander is the frozen Phase 7C command creation boundary. The recovery
// worker never inserts device-control rows directly.
type Commander interface {
	CreateSyncRequest(ctx context.Context, deviceID, idempotencyKey string) (CommandResult, error)
	ActiveCommand(ctx context.Context, deviceID string) (CommandResult, bool, error)
}

// CommandResult is the minimal 7C outcome the healer needs.
type CommandResult struct {
	ID string
}

// RecoveryWorker executes pending DEVICE_RECONNECT_SYNC actions through
// the frozen Phase 7C service. Queueing (or adopting) the command
// completes the action; terminal execution belongs to 7C/Retail.
type RecoveryWorker struct {
	store     Store
	commander Commander
	now       func() time.Time
	metrics   *Metrics
}

func NewRecoveryWorker(store Store, commander Commander, now func() time.Time, metrics *Metrics) *RecoveryWorker {
	if now == nil {
		now = time.Now
	}
	return &RecoveryWorker{store: store, commander: commander, now: now, metrics: metrics}
}

// ProcessOne claims one due action and resolves it. Only failures before
// Phase 7C durably resolves creation retry (bounded); semantic outcomes
// complete immediately.
func (w *RecoveryWorker) ProcessOne(ctx context.Context) (bool, error) {
	action, ok, err := w.store.ClaimRecovery(ctx, w.now().UTC())
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	now := w.now().UTC()
	incident, found, err := w.store.IncidentByID(ctx, action.IncidentID)
	if err != nil {
		return false, err
	}
	if !found {
		// Incident row gone (should not happen; no destructive FKs exist):
		// block rather than loop.
		if ferr := w.store.FinishRecoveryBlocked(ctx, action.ID, CodeRecoveryFailed, now); ferr != nil {
			return false, ferr
		}
		return true, nil
	}
	if incident.State == StateResolved && incident.ResolutionCode != nil && *incident.ResolutionCode == ResolutionNoActive {
		// Device revoked before recovery ran: never queue for inactive.
		if ferr := w.store.FinishRecoveryBlocked(ctx, action.ID, ResolutionNoActive, now); ferr != nil {
			return false, ferr
		}
		w.metrics.recoveryTotal.Add(1)
		return true, nil
	}
	result, err := w.commander.CreateSyncRequest(ctx, incident.SubjectID, action.IdempotencyKey)
	if err == nil {
		if ferr := w.store.FinishRecoveryCompleted(ctx, action.ID, result.ID, CodeRecoveryQueued, now); ferr != nil {
			return false, ferr
		}
		w.metrics.recoveryTotal.Add(1)
		return true, nil
	}
	if isAlreadyActive(err) {
		// Manual-vs-auto race converged on the existing command: adopt it.
		active, ok, aerr := w.commander.ActiveCommand(ctx, incident.SubjectID)
		if aerr != nil {
			return false, w.retryOrBlock(ctx, action, aerr, now)
		}
		if !ok {
			return false, w.retryOrBlock(ctx, action, err, now)
		}
		if ferr := w.store.FinishRecoveryCompleted(ctx, action.ID, active.ID, CodeRecoveryExisting, now); ferr != nil {
			return false, ferr
		}
		w.metrics.recoveryTotal.Add(1)
		return true, nil
	}
	return false, w.retryOrBlock(ctx, action, err, now)
}

func (w *RecoveryWorker) retryOrBlock(ctx context.Context, action Recovery, err error, now time.Time) error {
	if action.AttemptCount+1 >= config.MaxRecoveryAttempts {
		if ferr := w.store.FinishRecoveryBlocked(ctx, action.ID, CodeRecoveryFailed, now); ferr != nil {
			return ferr
		}
		w.metrics.recoveryTotal.Add(1)
		return nil
	}
	backoff := time.Duration(action.AttemptCount+1) * time.Minute
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	_ = err
	return w.store.RetryRecoveryLater(ctx, action.ID, CodeRecoveryFailed, now.Add(backoff), now)
}

func isAlreadyActive(err error) bool {
	var ae *apperr.Error
	if errors.As(err, &ae) && ae.Kind == apperr.Conflict {
		return ae.Message == "DEVICE_SYNC_ALREADY_ACTIVE"
	}
	return false
}
