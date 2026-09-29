package operations

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
)

// DeviceCommander adapts the frozen Phase 7C command service to the
// recovery Command boundary.
type DeviceCommander struct {
	svc *devicecontrol.Service
}

func NewDeviceCommander(svc *devicecontrol.Service) *DeviceCommander {
	return &DeviceCommander{svc: svc}
}

func (c *DeviceCommander) CreateSyncRequest(ctx context.Context, deviceID, idempotencyKey string) (CommandResult, error) {
	cmd, _, err := c.svc.CreateSyncRequest(ctx, deviceID, idempotencyKey)
	if err != nil {
		return CommandResult{}, err
	}
	return CommandResult{ID: cmd.ID}, nil
}

func (c *DeviceCommander) ActiveCommand(ctx context.Context, deviceID string) (CommandResult, bool, error) {
	cmd, ok, err := c.svc.Active(ctx, deviceID)
	if err != nil {
		return CommandResult{}, false, err
	}
	if !ok {
		return CommandResult{}, false, nil
	}
	return CommandResult{ID: cmd.ID}, true, nil
}

// Engine runs the bounded operations loops: detector scans, alert
// delivery drains, and recovery execution. One goroutine per loop, fixed
// batches, short transactions; multi-instance safety comes from database
// uniqueness, never process locks.
type Engine struct {
	detector *Detector
	alerts   *AlertProcessor
	recovery *RecoveryWorker
	interval time.Duration
	batch    int
	log      *slog.Logger
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	stopped  bool
	mu       sync.Mutex
}

func NewEngine(detector *Detector, alerts *AlertProcessor, recovery *RecoveryWorker, interval time.Duration, batch int, log *slog.Logger) *Engine {
	return &Engine{detector: detector, alerts: alerts, recovery: recovery, interval: interval, batch: batch, log: log,
		stop: make(chan struct{}), done: make(chan struct{})}
}

// Start begins the three bounded loops.
func (e *Engine) Start() {
	e.once.Do(func() { go e.run() })
}

// Stop shuts the loops down with a bounded wait. In-flight database work
// is short; no orphan goroutines remain.
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	e.stopped = true
	e.mu.Unlock()
	close(e.stop)
	select {
	case <-e.done:
	case <-time.After(30 * time.Second):
		e.log.Warn("operations engine shutdown timed out")
	}
}

func (e *Engine) run() {
	defer close(e.done)
	e.tick(context.Background())
	timer := time.NewTimer(e.interval)
	defer timer.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-timer.C:
			e.tick(context.Background())
			timer.Reset(e.interval)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	if err := e.detector.Scan(ctx); err != nil {
		e.log.Error("operations scan failed", "err", err.Error())
	}
	for i := 0; i < e.batch; i++ {
		more, err := e.alerts.ProcessOne(ctx)
		if err != nil {
			e.log.Error("operations alert drain failed", "err", err.Error())
			break
		}
		if !more {
			break
		}
	}
	for i := 0; i < e.batch; i++ {
		more, err := e.recovery.ProcessOne(ctx)
		if err != nil {
			e.log.Error("operations recovery drain failed", "err", err.Error())
			break
		}
		if !more {
			break
		}
	}
}
