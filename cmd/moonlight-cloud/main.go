// Command moonlight-cloud is the single MoonLightCloud binary.
//
//	moonlight-cloud serve                  run the HTTP server (default)
//	moonlight-cloud migrate up|status       explicit schema migrations
//	moonlight-cloud device create --name N  provision a device (secret shown once)
//	moonlight-cloud device revoke <id>      revoke a device
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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	adapterhttp "github.com/faroukelabady/MoonLightCloud/internal/adapter/http"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/app"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
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
	case "probe":
		return probe(args)
	case "dbprobe":
		return dbprobe()
	case "version", "--version", "-v":
		fmt.Printf("moonlight-cloud version=%s commit=%s build_time=%s\n", version, commit, buildTime)
		return nil
	default:
		return fmt.Errorf("unknown command %q: want serve|migrate|device|probe|version", cmd)
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

// serve runs the HTTP server with graceful SIGINT/SIGTERM shutdown:
// stop accepting, bounded in-flight completion, close server, close pool.
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
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.Log.Error("server error", "err", err.Error())
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	a.Log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownAfter)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	a.Log.Info("shutdown complete")
	return nil
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

func deviceCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moonlight-cloud device create|revoke ...")
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
	svc := auth.NewService(
		postgres.NewDevices(pool, cfg.DBQueryTimeout),
		auth.NewHasher(cfg.SecretPepper),
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
		fmt.Printf("device id: %s\ndevice secret: %s\n", p.Device.ID, p.RawSecret)
		fmt.Println("Store this securely. It cannot be recovered; the raw secret is shown only once.")
		return nil
	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("usage: moonlight-cloud device revoke <id>")
		}
		return svc.Revoke(ctx, args[1])
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
