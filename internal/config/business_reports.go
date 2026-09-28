package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BusinessReports holds the scheduled business-report scheduler
// configuration (Phase 7B). Disabled by default: no planner loop, no
// run worker, but CLI configuration remains available and Cloud stays
// healthy.
type BusinessReports struct {
	// Enabled turns the scheduler planner and run worker on.
	Enabled bool
	// PollInterval is the steady-state scan cadence for due schedules
	// and runs.
	PollInterval time.Duration
	// BatchSize bounds one planner scan pass.
	BatchSize int32
	// LeaseDuration bounds one run claim: crashed workers release
	// their runs automatically after this horizon.
	LeaseDuration time.Duration
}

// Business report scheduler values.
const (
	DefaultBusinessReportsPollInterval = 60 * time.Second
	MinBusinessReportsPollInterval     = 5 * time.Second
	MaxBusinessReportsPollInterval     = time.Hour
	DefaultBusinessReportsBatchSize    = 25
	MinBusinessReportsBatchSize        = 1
	MaxBusinessReportsBatchSize        = 100
	DefaultBusinessReportsLease        = 5 * time.Minute
	MinBusinessReportsLease            = time.Minute
	MaxBusinessReportsLease            = time.Hour
)

// loadBusinessReports reads BUSINESS_REPORTS_* environment variables.
// All validation errors are value-free by construction: raw supplied
// values are never echoed.
func loadBusinessReports() (BusinessReports, error) {
	enabled, err := parseBusinessReportsBoolFlag("BUSINESS_REPORTS_ENABLED")
	if err != nil {
		return BusinessReports{}, err
	}
	cfg := BusinessReports{
		Enabled:       enabled,
		PollInterval:  DefaultBusinessReportsPollInterval,
		BatchSize:     DefaultBusinessReportsBatchSize,
		LeaseDuration: DefaultBusinessReportsLease,
	}
	if v := strings.TrimSpace(os.Getenv("BUSINESS_REPORTS_POLL_INTERVAL")); v != "" {
		interval, err := time.ParseDuration(v)
		if err != nil {
			return BusinessReports{}, fmt.Errorf("invalid BUSINESS_REPORTS_POLL_INTERVAL: want a valid duration")
		}
		cfg.PollInterval = interval
	}
	if v := strings.TrimSpace(os.Getenv("BUSINESS_REPORTS_BATCH_SIZE")); v != "" {
		batch, err := strconv.Atoi(v)
		if err != nil {
			return BusinessReports{}, fmt.Errorf("invalid BUSINESS_REPORTS_BATCH_SIZE: want an integer")
		}
		cfg.BatchSize = int32(batch)
	}
	if v := strings.TrimSpace(os.Getenv("BUSINESS_REPORTS_LEASE_DURATION")); v != "" {
		lease, err := time.ParseDuration(v)
		if err != nil {
			return BusinessReports{}, fmt.Errorf("invalid BUSINESS_REPORTS_LEASE_DURATION: want a valid duration")
		}
		cfg.LeaseDuration = lease
	}
	if err := cfg.validate(); err != nil {
		return BusinessReports{}, err
	}
	return cfg, nil
}

// validate bounds scheduler tuning. Error text names keys and rules
// without printing values.
func (c BusinessReports) validate() error {
	if c.PollInterval < MinBusinessReportsPollInterval || c.PollInterval > MaxBusinessReportsPollInterval {
		return fmt.Errorf("BUSINESS_REPORTS_POLL_INTERVAL must be within [%s, %s]",
			MinBusinessReportsPollInterval, MaxBusinessReportsPollInterval)
	}
	if c.BatchSize < MinBusinessReportsBatchSize || c.BatchSize > MaxBusinessReportsBatchSize {
		return fmt.Errorf("BUSINESS_REPORTS_BATCH_SIZE must be within [%d, %d]",
			MinBusinessReportsBatchSize, MaxBusinessReportsBatchSize)
	}
	if c.LeaseDuration < MinBusinessReportsLease || c.LeaseDuration > MaxBusinessReportsLease {
		return fmt.Errorf("BUSINESS_REPORTS_LEASE_DURATION must be within [%s, %s]",
			MinBusinessReportsLease, MaxBusinessReportsLease)
	}
	return nil
}

// parseBusinessReportsBoolFlag parses one scheduler boolean without
// echoing the raw value into startup errors.
func parseBusinessReportsBoolFlag(key string) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	switch {
	case v == "":
		return false, nil
	case strings.EqualFold(v, "true") || v == "1":
		return true, nil
	case strings.EqualFold(v, "false") || v == "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s: want true|false|1|0", key)
	}
}
