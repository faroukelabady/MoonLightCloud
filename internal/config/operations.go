package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Operations holds the Phase 7D operational incident/alert configuration.
// Disabled by default: no detector, no recovery worker, no provider
// traffic. Alerting and automatic healing are independently gated so
// operators can observe before enabling reconnect sync.
type Operations struct {
	Enabled                     bool
	AutoSyncOnReconnect         bool
	ScanInterval                time.Duration
	ScanBatchSize               int
	DeviceOfflineAfter          time.Duration
	SyncPendingStaleAfter       time.Duration
	SyncRunningStaleAfter       time.Duration
	ReportStaleAfter            time.Duration
	NotificationRetryStaleAfter time.Duration
}

const (
	defaultOpsScanInterval   = 60 * time.Second
	defaultOpsBatchSize      = 100
	defaultOfflineAfter      = 5 * time.Minute
	defaultSyncPendingStale  = 30 * time.Minute
	defaultSyncRunningStale  = 30 * time.Minute
	defaultReportStale       = 60 * time.Minute
	defaultNotificationStale = 60 * time.Minute
	minOpsScanInterval       = 10 * time.Second
	maxOpsScanInterval       = 30 * time.Minute
	minOpsThreshold          = 30 * time.Second
	maxOpsThreshold          = 24 * time.Hour
	minOpsBatchSize          = 1
	maxOpsBatchSize          = 1000
	maxRecoveryAttempts      = 10
)

// RecoveryAttempts bounds transient recovery retries before blocking.
const MaxRecoveryAttempts = maxRecoveryAttempts

func loadOperations() (Operations, error) {
	c := Operations{
		ScanInterval: defaultOpsScanInterval, ScanBatchSize: defaultOpsBatchSize,
		DeviceOfflineAfter: defaultOfflineAfter, SyncPendingStaleAfter: defaultSyncPendingStale,
		SyncRunningStaleAfter: defaultSyncRunningStale, ReportStaleAfter: defaultReportStale,
		NotificationRetryStaleAfter: defaultNotificationStale,
	}
	var err error
	if c.Enabled, err = parseBoolFlag("OPERATIONS_ENABLED"); err != nil {
		return Operations{}, err
	}
	if c.AutoSyncOnReconnect, err = parseBoolFlag("OPERATIONS_AUTO_SYNC_ON_RECONNECT"); err != nil {
		return Operations{}, err
	}
	if c.ScanInterval, err = parseOpsDuration("OPERATIONS_SCAN_INTERVAL", c.ScanInterval, minOpsScanInterval, maxOpsScanInterval); err != nil {
		return Operations{}, err
	}
	if c.ScanBatchSize, err = parseOpsCount("OPERATIONS_SCAN_BATCH_SIZE", c.ScanBatchSize); err != nil {
		return Operations{}, err
	}
	if c.DeviceOfflineAfter, err = parseOpsDuration("OPERATIONS_DEVICE_OFFLINE_AFTER", c.DeviceOfflineAfter, minOpsThreshold, maxOpsThreshold); err != nil {
		return Operations{}, err
	}
	if c.SyncPendingStaleAfter, err = parseOpsDuration("OPERATIONS_SYNC_PENDING_STALE_AFTER", c.SyncPendingStaleAfter, minOpsThreshold, maxOpsThreshold); err != nil {
		return Operations{}, err
	}
	if c.SyncRunningStaleAfter, err = parseOpsDuration("OPERATIONS_SYNC_RUNNING_STALE_AFTER", c.SyncRunningStaleAfter, minOpsThreshold, maxOpsThreshold); err != nil {
		return Operations{}, err
	}
	if c.ReportStaleAfter, err = parseOpsDuration("OPERATIONS_REPORT_STALE_AFTER", c.ReportStaleAfter, minOpsThreshold, maxOpsThreshold); err != nil {
		return Operations{}, err
	}
	if c.NotificationRetryStaleAfter, err = parseOpsDuration("OPERATIONS_NOTIFICATION_RETRY_STALE_AFTER", c.NotificationRetryStaleAfter, minOpsThreshold, maxOpsThreshold); err != nil {
		return Operations{}, err
	}
	return c, nil
}

// parseOpsDuration parses wide (time.ParseDuration is int64-based) then
// range-checks before use. Errors are value-free.
func parseOpsDuration(key string, def, min, max time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min || d > max {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return d, nil
}

// parseOpsCount parses as 64-bit before narrowing to int, so very large
// integers fail closed instead of overflowing.
func parseOpsCount(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < minOpsBatchSize || n > maxOpsBatchSize {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return int(n), nil
}

// checkOpsThreshold enforces the offline/online coverage invariant with a
// value-free diagnostic.
func (c *Config) checkOpsThreshold() error {
	if c.Operations.DeviceOfflineAfter < c.DeviceControl.OnlineWindow {
		return fmt.Errorf("invalid OPERATIONS_DEVICE_OFFLINE_AFTER")
	}
	return nil
}
