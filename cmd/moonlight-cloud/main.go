// Command moonlight-cloud is the single MoonLightCloud binary.
//
//	moonlight-cloud serve                  run the HTTP server (default)
//	moonlight-cloud migrate up|status       explicit schema migrations
//	moonlight-cloud device create --name N  provision a device (credential shown once)
//	moonlight-cloud device list             safe operator visibility (no secrets)
//	moonlight-cloud device rotate <id>      rotate a device credential (new secret once)
//	moonlight-cloud device revoke <id>      revoke a device and all its credentials
//	moonlight-cloud projection status       projector diagnostics (safe counts)
//	moonlight-cloud projection retry <id> [processor]  return one event to pending
//	moonlight-cloud probe --url U           single health probe (container healthcheck)
//
// Version metadata injects at build time:
//
//	go build -ldflags "-X main.version=... -X main.commit=... -X main.buildTime=..."
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	// Embedded IANA timezone database: the distroless runtime ships no
	// zoneinfo files, so Africa/Cairo (and any STORE_TIMEZONE) resolves
	// deterministically inside the production image.
	_ "time/tzdata"

	_ "github.com/jackc/pgx/v5/stdlib"

	adapterhttp "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/app"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// Build metadata (ldflags).
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	// Keep app package metadata in sync for /version.
	app.Version = version
	app.Commit = commit
	app.BuildTime = buildTime
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !isFlag(args[0]) {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args)
	case "migrate":
		return migrateCmd(args)
	case "device":
		return deviceCmd(args)
	case "projection":
		return projectionCmd(args)
	case "dashboard":
		return dashboardCmd(args)
	case "probe":
		return probe(args)
	case "dbprobe":
		return dbprobe()
	case "version", "--version", "-v":
		fmt.Printf("moonlight-cloud version=%s commit=%s build_time=%s\n", version, commit, buildTime)
		return nil
	default:
		return fmt.Errorf("unknown command %q: want serve|migrate|device|projection|dashboard|probe|version", cmd)
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

// serve runs the HTTP server with graceful SIGINT/SIGTERM shutdown:
// stop accepting, bounded in-flight completion, close server, close pool.
// Server errors propagate through runServer to main orchestration; the
// server goroutine never calls os.Exit so deferred cleanup always runs.
func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	doMigrate := fs.Bool("migrate", false, "run pending migrations before serving (explicit operator opt-in)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if *doMigrate {
		if err := migrateUp(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
	}
	a, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()
	srv := adapterhttp.Server(cfg.HTTPAddr, a.Handler, adapterhttp.Config{})
	a.Log.Info("listening", "addr", cfg.HTTPAddr, "env", cfg.Environment,
		"version", version, "commit", commit)
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Projector starts only after config/DB/schema/app wiring are ready; it
	// obeys the same lifecycle context and stops claiming on cancellation.
	projCtx, projCancel := context.WithCancel(sigCtx)
	defer projCancel()
	go a.Projector.Run(projCtx)
	go a.ReturnProjector.Run(projCtx)
	go a.CategoryProjector.Run(projCtx)
	go a.TagProjector.Run(projCtx)
	go a.ProductProjector.Run(projCtx)
	if err := runServer(sigCtx, srv, cfg.ShutdownAfter, a.Log); err != nil {
		return err
	}
	a.Log.Info("shutdown complete")
	return nil
}

// runServer orchestrates one http.Server: serve in a goroutine, report
// bind/serve errors on the error channel, shut down gracefully on context
// cancellation with a bounded timeout. http.ErrServerClosed after Shutdown
// is clean (nil). No os.Exit anywhere on this path.
func runServer(ctx context.Context, srv *http.Server, shutdownAfter time.Duration, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		// Server failed before any shutdown signal (e.g. port occupied).
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownAfter)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		if err := <-errCh; err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}
}

func migrateUp(ctx context.Context, databaseURL string) error {
	conn, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer conn.Close()
	return migrate.Up(ctx, conn)
}

func migrateCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud migrate up|status")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	conn, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	switch args[0] {
	case "up":
		return migrate.Up(ctx, conn)
	case "status":
		v, err := migrate.Current(ctx, conn)
		if err != nil {
			return err
		}
		fmt.Printf("schema version: %d (target %d)\n", v, migrate.TargetVersion)
		return nil
	default:
		return fmt.Errorf("unknown migrate subcommand %q", args[0])
	}
}

// dashboardCmd hosts operator helpers. hash-password reads a password from
// stdin (never argv, never logs) and prints the Argon2id PHC string for
// DASHBOARD_PASSWORD_HASH provisioning.
func dashboardCmd(args []string) error {
	if len(args) == 0 || args[0] != "hash-password" {
		return fmt.Errorf("usage: moonlight-cloud dashboard hash-password < /dev/stdin")
	}
	var pw strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			pw.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	password := strings.TrimSpace(pw.String())
	if password == "" {
		return fmt.Errorf("empty password on stdin")
	}
	hash, err := dashboard.HashPassword(password)
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}

// projectionCmd is the small operator surface for both projectors:
// status shows pending/retry/blocked/processed counts, oldest pending age,
// and the last error per processor; retry returns one blocked/retryable
// event to pending for the given processor (default: sale).
// The immutable source event is never touched. No destructive fix exists.
func projectionCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud projection status|retry <event-id>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewDevices(pool, cfg.DBQueryTimeout)
	switch args[0] {
	case "status":
		for _, processor := range []string{sale.ProcessorSaleProjectionV1, returnrefund.ProcessorReturnProjectionV1, catalog.ProcessorCategoryProjectionV1, catalog.ProcessorTagProjectionV1, catalog.ProcessorProductProjectionV1} {
			stats, err := store.ProcessingStats(ctx, processor)
			if err != nil {
				return err
			}
			fmt.Printf("processor: %s\n", processor)
			for _, s := range []string{sale.ProcPending, sale.ProcRetry, sale.ProcBlocked, sale.ProcProcessed} {
				fmt.Printf("  %-9s %d\n", s, stats.Counts[s])
			}
			if stats.OldestPending != nil {
				fmt.Printf("oldest pending: %s (age %s)\n",
					stats.OldestPending.UTC().Format(time.RFC3339),
					time.Since(*stats.OldestPending).Round(time.Second))
			} else {
				fmt.Println("oldest pending: -")
			}
			if stats.LastErrorCode != "" {
				fmt.Printf("last error: %s %s: %s\n", stats.LastErrorEvent, stats.LastErrorCode, stats.LastErrorMsg)
			} else {
				fmt.Println("last error: -")
			}
		}
		return nil
	case "retry":
		if len(args) < 2 {
			return fmt.Errorf("usage: moonlight-cloud projection retry <event-id> [processor]")
		}
		processor := sale.ProcessorSaleProjectionV1
		if len(args) >= 3 {
			processor = args[2]
		}
		validProcessors := map[string]bool{
			sale.ProcessorSaleProjectionV1: true, returnrefund.ProcessorReturnProjectionV1: true,
			catalog.ProcessorCategoryProjectionV1: true, catalog.ProcessorTagProjectionV1: true,
			catalog.ProcessorProductProjectionV1: true,
		}
		if !validProcessors[processor] {
			return fmt.Errorf("unknown processor %q", processor)
		}
		return store.ResetProcessing(ctx, processor, args[1])
	default:
		return fmt.Errorf("unknown projection subcommand %q", args[0])
	}
}

func deviceCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud device create|list|rotate|revoke ...")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	hasher, err := auth.NewHasher(cfg.Pepper)
	if err != nil {
		return err
	}
	svc := auth.NewService(
		postgres.NewDevices(pool, cfg.DBQueryTimeout),
		hasher, cfg.PepperRaw, cfg.PepperVersion,
		clock.System{}, ids.System{},
	)
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		name := fs.String("name", "", "device name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		p, err := svc.Create(ctx, *name)
		if err != nil {
			return err
		}
		fmt.Println("Device created.")
		fmt.Printf("\nDevice ID:\n%s\n", p.Device.ID)
		fmt.Printf("\nCredential ID:\n%s\n", p.Credential.ID)
		fmt.Printf("\nCredential:\n%s.%s.%s\n", p.Device.ID, p.Credential.ID, p.RawSecret)
		fmt.Println("\nStore this credential securely. It cannot be recovered.")
		return nil
	case "list":
		devs, creds, err := svc.List(ctx)
		if err != nil {
			return err
		}
		for _, d := range devs {
			active, revoked := 0, 0
			for _, c := range creds[d.ID] {
				if c.Active() {
					active++
				} else {
					revoked++
				}
			}
			lastSeen := "-"
			if d.LastSeenAt != nil {
				lastSeen = d.LastSeenAt.UTC().Format(time.RFC3339)
			}
			fmt.Printf("%s  %-20s  %-8s  created=%s  last_seen=%s  credentials=active:%d revoked:%d\n",
				d.ID, d.Name, d.Status,
				d.CreatedAt.UTC().Format(time.RFC3339), lastSeen, active, revoked)
		}
		return nil
	case "rotate":
		if len(args) < 2 {
			return fmt.Errorf("usage: moonlight-cloud device rotate <device-id>")
		}
		p, err := svc.Rotate(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Println("Credential rotated. The previous credential no longer works.")
		fmt.Printf("\nDevice ID:\n%s\n", p.Device.ID)
		fmt.Printf("\nNew credential ID:\n%s\n", p.Credential.ID)
		fmt.Printf("\nNew credential:\n%s.%s.%s\n", p.Device.ID, p.Credential.ID, p.RawSecret)
		fmt.Println("\nStore this credential securely. It cannot be recovered.")
		return nil
	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("usage: moonlight-cloud device revoke <device-id>")
		}
		return svc.RevokeDevice(ctx, args[1])
	case "revoke-credential":
		if len(args) < 2 {
			return fmt.Errorf("usage: moonlight-cloud device revoke-credential <credential-id>")
		}
		return svc.RevokeCredential(ctx, args[1])
	default:
		return fmt.Errorf("unknown device subcommand %q", args[0])
	}
}

// dbprobe opens the pool and pings PostgreSQL once; exit 0/1.
// Used by scripts to wait for real readiness (TCP accept is not enough).
func dbprobe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	return nil
}

// probe performs one GET and exits 0/1 for container HEALTHCHECK use.
func probe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	url := fs.String("url", "http://localhost:8080/health/live", "probe URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(*url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe status %d", resp.StatusCode)
	}
	return nil
}
