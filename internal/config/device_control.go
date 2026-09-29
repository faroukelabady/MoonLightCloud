package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// DeviceControl holds Phase 7C control-plane configuration. Disabled by
// default: no polling surface behavior changes until explicitly enabled.
type DeviceControl struct {
	Enabled       bool
	LeaseDuration time.Duration
	OnlineWindow  time.Duration
}

const (
	defaultDeviceLease  = 60 * time.Second
	defaultOnlineWindow = 60 * time.Second
	minDeviceLease      = 10 * time.Second
	maxDeviceLease      = 10 * time.Minute
	minOnlineWindow     = 10 * time.Second
	maxOnlineWindow     = 10 * time.Minute
)

func loadDeviceControl() (DeviceControl, error) {
	c := DeviceControl{LeaseDuration: defaultDeviceLease, OnlineWindow: defaultOnlineWindow}
	if v := strings.TrimSpace(os.Getenv("DEVICE_CONTROL_ENABLED")); v != "" {
		b, err := parseBoolFlag("DEVICE_CONTROL_ENABLED")
		if err != nil {
			return DeviceControl{}, err
		}
		c.Enabled = b
	}
	if v := strings.TrimSpace(os.Getenv("DEVICE_COMMAND_LEASE_DURATION")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return DeviceControl{}, fmt.Errorf("invalid DEVICE_COMMAND_LEASE_DURATION")
		}
		c.LeaseDuration = d
	}
	if v := strings.TrimSpace(os.Getenv("DEVICE_ONLINE_WINDOW")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return DeviceControl{}, fmt.Errorf("invalid DEVICE_ONLINE_WINDOW")
		}
		c.OnlineWindow = d
	}
	if c.LeaseDuration < minDeviceLease || c.LeaseDuration > maxDeviceLease {
		return DeviceControl{}, fmt.Errorf("invalid DEVICE_COMMAND_LEASE_DURATION")
	}
	if c.OnlineWindow < minOnlineWindow || c.OnlineWindow > maxOnlineWindow {
		return DeviceControl{}, fmt.Errorf("invalid DEVICE_ONLINE_WINDOW")
	}
	return c, nil
}
