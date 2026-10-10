package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// SupportBundle is the sanitized Cloud diagnostic export (Phase 20 §34).
//
// Allowlist only. It deliberately excludes: passwords, tokens, private
// keys, MFA secrets, recovery codes, provider credentials, customer
// records, and database contents. The bundle is safe to attach to a
// support ticket; the negative tests assert that property.
type SupportBundle struct {
	CollectedAt   time.Time           `json:"collected_at"`
	App           string              `json:"app"`
	Version       string              `json:"version"`
	Commit        string              `json:"commit"`
	GoVersion     string              `json:"go_version"`
	GOOS          string              `json:"goos"`
	GOARCH        string              `json:"goarch"`
	SchemaVersion int64               `json:"schema_version"`
	TargetSchema  int64               `json:"target_schema"`
	DatabaseOK    bool                `json:"database_ok"`
	DatabaseError string              `json:"database_error,omitempty"`
	Projection    []ProjectionSummary `json:"projection"`
	Environment   EnvironmentSummary  `json:"environment"`
	RedactionNote string              `json:"redaction_note"`
}

// ProjectionSummary is bounded projector state: counts and codes only,
// never event payloads.
type ProjectionSummary struct {
	Processor     string `json:"processor"`
	Pending       int64  `json:"pending"`
	Retry         int64  `json:"retry"`
	Blocked       int64  `json:"blocked"`
	Processed     int64  `json:"processed"`
	OldestPending string `json:"oldest_pending,omitempty"`
	LastErrorCode string `json:"last_error_code,omitempty"`
}

// EnvironmentSummary records which optional features are enabled. It
// records booleans only: no hostnames, no credentials, no URLs.
type EnvironmentSummary struct {
	Environment        string `json:"environment"`
	DeviceControl      bool   `json:"device_control"`
	BusinessReports    bool   `json:"business_reports"`
	Operations         bool   `json:"operations"`
	CatalogAdmin       bool   `json:"catalog_admin"`
	WhatsAppNotifs     bool   `json:"whatsapp_notifications"`
	TelegramNotifs     bool   `json:"telegram_notifications"`
	ReleaseTrustLoaded bool   `json:"release_trust_loaded"`
	MFAKeyConfigured   bool   `json:"mfa_key_configured"`
}

// diagnosticsCmd writes the sanitized bundle to stdout or --out.
func diagnosticsCmd(args []string) error {
	out, err := parseDiagnosticsArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	bundle := SupportBundle{
		CollectedAt:   time.Now().UTC(),
		App:           "moonlight-cloud",
		Version:       version,
		Commit:        commit,
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		TargetSchema:  migrate.TargetVersion,
		RedactionNote: "Allowlisted fields only. No passwords, tokens, keys, MFA secrets, recovery codes, provider credentials or business records are included.",
		Environment: EnvironmentSummary{
			Environment:      cfg.Environment,
			DeviceControl:    cfg.DeviceControl.Enabled,
			BusinessReports:  cfg.BusinessReports.Enabled,
			Operations:       cfg.Operations.Enabled,
			CatalogAdmin:     true,
			WhatsAppNotifs:   cfg.WhatsAppNotifications.Enabled,
			TelegramNotifs:   cfg.TelegramNotifications.Enabled,
			MFAKeyConfigured: cfg.AuthMFAEncryptionKey != "",
		},
	}
	// Record whether release trust is configured WITHOUT exposing keys.
	bundle.Environment.ReleaseTrustLoaded = len(cfg.ReleaseTrustedPublicKeys) > 0

	ctx := context.Background()
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		bundle.DatabaseOK = false
		bundle.DatabaseError = "database unreachable"
		return writeBundle(out, bundle)
	}
	defer pool.Close()
	bundle.DatabaseOK = true

	conn, err := sql.Open("pgx", cfg.DatabaseURL)
	if err == nil {
		defer conn.Close()
		if current, err := migrate.Current(ctx, conn); err == nil {
			bundle.SchemaVersion = current
		}
	}

	store := postgres.NewDevices(pool, cfg.DBQueryTimeout)
	for _, processor := range allProjectionProcessors() {
		stats, err := store.ProcessingStats(ctx, processor)
		if err != nil {
			continue
		}
		summary := ProjectionSummary{
			Processor: processor,
			Pending:   stats.Counts[sale.ProcPending],
			Retry:     stats.Counts[sale.ProcRetry],
			Blocked:   stats.Counts[sale.ProcBlocked],
			Processed: stats.Counts[sale.ProcProcessed],
		}
		if stats.OldestPending != nil {
			summary.OldestPending = stats.OldestPending.UTC().Format(time.RFC3339)
		}
		// Only the machine code crosses the boundary - never the message.
		summary.LastErrorCode = stats.LastErrorCode
		bundle.Projection = append(bundle.Projection, summary)
	}
	return writeBundle(out, bundle)
}

func writeBundle(out string, bundle SupportBundle) error {
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if out == "" {
		_, err = fmt.Println(string(raw))
		return err
	}
	return os.WriteFile(out, append(raw, '\n'), 0o600)
}

// parseDiagnosticsArgs accepts only `--out <file>` (or nothing). Unknown
// or incomplete arguments are an error, never silently ignored.
func parseDiagnosticsArgs(args []string) (string, error) {
	out := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--out" && i+1 < len(args) && args[i+1] != "":
			out = args[i+1]
			i++
		default:
			return "", fmt.Errorf("usage: moonlight-cloud diagnostics [--out <file>]")
		}
	}
	return out, nil
}
