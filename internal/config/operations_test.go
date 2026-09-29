package config

import (
	"strings"
	"testing"
	"time"
)

func setOperationsEnv(t *testing.T, values map[string]string) {
	t.Helper()
	keys := []string{
		"OPERATIONS_ENABLED", "OPERATIONS_AUTO_SYNC_ON_RECONNECT",
		"OPERATIONS_SCAN_INTERVAL", "OPERATIONS_SCAN_BATCH_SIZE",
		"OPERATIONS_DEVICE_OFFLINE_AFTER", "OPERATIONS_SYNC_PENDING_STALE_AFTER",
		"OPERATIONS_SYNC_RUNNING_STALE_AFTER", "OPERATIONS_REPORT_STALE_AFTER",
		"OPERATIONS_NOTIFICATION_RETRY_STALE_AFTER",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestOperationsConfig(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		setOperationsEnv(t, map[string]string{})
		cfg, err := loadOperations()
		if err != nil {
			t.Fatalf("disabled: %v", err)
		}
		if cfg.Enabled || cfg.AutoSyncOnReconnect || cfg.ScanBatchSize != 100 {
			t.Fatalf("defaults: %+v", cfg)
		}
	})
	t.Run("wide values", func(t *testing.T) {
		for value, want := range map[string]int{
			"0": 0, "1": 1, "100": 100, "1000": 1000,
		} {
			setOperationsEnv(t, map[string]string{"OPERATIONS_SCAN_BATCH_SIZE": value})
			cfg, err := loadOperations()
			if want == 0 || want > 1000 {
				if err == nil {
					t.Fatalf("%s must fail", value)
				}
				continue
			}
			if err != nil || cfg.ScanBatchSize != want {
				t.Fatalf("%s: %+v %v", value, cfg, err)
			}
		}
		for _, value := range []string{"1001", "99999999999999999999", "nonnumeric", "-5"} {
			setOperationsEnv(t, map[string]string{"OPERATIONS_SCAN_BATCH_SIZE": value})
			if _, err := loadOperations(); err == nil {
				t.Fatalf("%s must fail", value)
			} else if strings.Contains(err.Error(), value) && value != "nonnumeric" {
				t.Fatalf("leaked: %q", err.Error())
			}
		}
	})
	t.Run("duration bounds value-free", func(t *testing.T) {
		for key, value := range map[string]string{
			"OPERATIONS_SCAN_INTERVAL":            "SECRET_VALUE",
			"OPERATIONS_DEVICE_OFFLINE_AFTER":     "1s",
			"OPERATIONS_SYNC_PENDING_STALE_AFTER": "999h",
		} {
			setOperationsEnv(t, map[string]string{key: value})
			_, err := loadOperations()
			if err == nil {
				t.Fatalf("%s must fail", key)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("leaked: %q", err.Error())
			}
		}
	})
	t.Run("offline threshold covers online window", func(t *testing.T) {
		c := Config{DeviceControl: DeviceControl{OnlineWindow: time.Minute},
			Operations: Operations{DeviceOfflineAfter: 30 * time.Second}}
		if err := c.checkOpsThreshold(); err == nil {
			t.Fatal("offline below online window must fail")
		}
		c.Operations.DeviceOfflineAfter = time.Minute
		if err := c.checkOpsThreshold(); err != nil {
			t.Fatalf("equal covers: %v", err)
		}
	})
}
