package migrate_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// These doubles exercise shell control flow only. Real PostgreSQL restore and
// migration correctness are covered separately by integration verification.
type restoreScriptResult struct {
	output string
	calls  string
	err    error
}

func runRestoreScript(t *testing.T, archive, restored string, binary, verify, unmarked bool, extraEnv ...string) restoreScriptResult {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("pg_restore", `#!/bin/bash
printf 'pg_restore %s\n' "$*" >> "$FAKE_CALLS"
case "$1" in
  --list) exit 0 ;;
  --data-only)
    printf 'COPY public.goose_db_version (id, version_id, is_applied, tstamp) FROM stdin;\n'
    if [[ "$FAKE_ARCHIVE" != empty ]]; then printf '1\t%s\tt\t2026-11-10\n' "$FAKE_ARCHIVE"; fi
    printf '\\.\n'
    ;;
  --clean) exit "${FAKE_RESTORE_RC:-0}" ;;
  *) exit 90 ;;
esac
`)
	write("psql", `#!/bin/bash
printf 'psql %s\n' "$*" >> "$FAKE_CALLS"
case "$*" in
  *pg_catalog.pg_tables*) printf '%s\n' "${FAKE_EXISTING:-0}" ;;
  *'max(version_id)'*) printf '%s\n' "$FAKE_RESTORED" ;;
  *'SELECT id FROM devices'*) ;;
  *'LEFT JOIN stores'*|*'LEFT JOIN sync_recovery_state'*|*'SELECT count(*) FROM devices'*|*'SELECT count(*) FROM sync_recovery_state'*) echo 0 ;;
  *) echo 1 ;;
esac
`)
	cloud := write("moonlight-cloud", `#!/bin/bash
printf 'cloud %s\n' "$*" >> "$FAKE_CALLS"
[[ "$*" == 'migrate up' ]] || exit 91
exit "${FAKE_MIGRATE_RC:-0}"
`)
	dump := write("backup.dump", "test archive: command doubles only\n")
	log := write("calls", "")
	args := []string{"../../scripts/pg-restore.sh", "--url", "postgres://restore-control-flow.invalid/test", "--dump", dump}
	if binary {
		args = append(args, "--cloud-binary", cloud)
	}
	if verify {
		args = append(args, "--verify-only")
	}
	if unmarked {
		args = append(args, "--allow-unmarked-restore", "--confirm-unmarked-restore")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", args...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PG_RESTORE_MARK_RECOVERY=") && !strings.HasPrefix(value, "FAKE_") && !strings.HasPrefix(value, "PATH=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+dir+":"+os.Getenv("PATH"), "FAKE_CALLS="+log, "FAKE_ARCHIVE="+archive, "FAKE_RESTORED="+restored)
	cmd.Env = append(cmd.Env, extraEnv...)
	output, err := cmd.CombinedOutput()
	calls, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return restoreScriptResult{string(output), string(calls), err}
}

func TestRestoreScriptSchemaConstantsMatchMigrationHistory(t *testing.T) {
	script, err := os.ReadFile("../../scripts/pg-restore.sh")
	if err != nil {
		t.Fatal(err)
	}
	constant := func(name string) int64 {
		t.Helper()
		match := regexp.MustCompile(`(?m)^` + name + `=([0-9]+)$`).FindSubmatch(script)
		if match == nil {
			t.Fatalf("missing %s in restore script", name)
		}
		value, err := strconv.ParseInt(string(match[1]), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := constant("TARGET_SCHEMA"); got != migrate.TargetVersion {
		t.Fatalf("restore target %d differs from migrate.TargetVersion %d", got, migrate.TargetVersion)
	}
	baselines, err := filepath.Glob("../../db/migrations/*_baseline.sql")
	if err != nil || len(baselines) != 1 {
		t.Fatalf("want one baseline migration, got %v: %v", baselines, err)
	}
	baseline, err := strconv.ParseInt(strings.Split(filepath.Base(baselines[0]), "_")[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if got := constant("BASELINE_SCHEMA"); got != baseline {
		t.Fatalf("restore baseline %d differs from actual baseline %d", got, baseline)
	}
}

func TestRestoreScriptRefusesInvalidCloudBinary(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory=%t", directory), func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			for _, tool := range []string{"psql", "pg_restore"} {
				if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/bash\necho unexpected >> \"$FAKE_CALLS\"\nexit 90\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cloud := filepath.Join(dir, "moonlight-cloud")
			var err error
			if directory {
				err = os.Mkdir(cloud, 0700)
			} else {
				err = os.WriteFile(cloud, []byte("#!/bin/bash\nexit 0\n"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			dump := filepath.Join(dir, "backup.dump")
			if err := os.WriteFile(dump, []byte("command doubles only\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/bash", "../../scripts/pg-restore.sh", "--url", "postgres://restore-control-flow.invalid/test", "--dump", dump, "--cloud-binary", cloud)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FAKE_CALLS="+log, "PG_RESTORE_MARK_RECOVERY=1")
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "--cloud-binary is not an executable file") {
				t.Fatalf("invalid binary not refused: %v\n%s", err, output)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("invalid binary invoked a database command: %v", err)
			}
		})
	}
}

func TestRestoreScriptArchiveSchemaPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, archive string
		binary, allow bool
	}{
		{"target", "118", false, true},
		{"baseline-needs-binary", "117", false, false},
		{"baseline-with-binary", "117", true, true},
		{"old-marking-schema", "109", false, false},
		{"old-marking-schema-with-binary", "109", true, false},
		{"pre-baseline", "116", false, false},
		{"pre-baseline-with-binary", "116", true, false},
		{"future", "119", false, false},
		{"future-with-binary", "119", true, false},
		{"zero", "0", true, false},
		{"empty", "empty", true, false},
		{"invalid", "invalid", true, false},
		{"numeric-prefix-invalid", "118garbage", true, false},
	} {
		for _, unmarked := range []bool{false, true} {
			for _, verify := range []bool{false, true} {
				// Successful full restores are checked with the postcheck cases below.
				if tc.allow && !verify {
					continue
				}
				t.Run(fmt.Sprintf("%s/unmarked=%t/verify=%t", tc.name, unmarked, verify), func(t *testing.T) {
					r := runRestoreScript(t, tc.archive, "118", tc.binary, verify, unmarked)
					if (r.err == nil) != tc.allow {
						t.Fatalf("allow=%t, error=%v\n%s", tc.allow, r.err, r.output)
					}
					if strings.Contains(r.calls, "psql ") || strings.Contains(r.calls, "pg_restore --clean") || strings.Contains(r.calls, "cloud ") {
						t.Fatalf("preflight touched target or ran migration:\n%s", r.calls)
					}
					if tc.allow && !strings.Contains(r.output, "verify-only OK") {
						t.Fatalf("successful preflight missing confirmation:\n%s", r.output)
					}
				})
			}
		}
	}
}

func TestRestoreScriptPostcheckRequiresExactTarget(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		archive := "118"
		if baseline {
			archive = "117"
		}
		for _, unmarked := range []bool{false, true} {
			for _, restored := range []string{"118", "117", "109", "119", "0", "invalid"} {
				t.Run(fmt.Sprintf("archive=%s/restored=%s/unmarked=%t", archive, restored, unmarked), func(t *testing.T) {
					r := runRestoreScript(t, archive, restored, baseline, false, unmarked)
					if (r.err == nil) != (restored == "118") {
						t.Fatalf("restored=%s, error=%v\n%s", restored, r.err, r.output)
					}
					postcheck := strings.Index(r.calls, "max(version_id)")
					restore := strings.Index(r.calls, "pg_restore --clean")
					if postcheck < 0 || restore < 0 || restore > postcheck {
						t.Fatalf("missing post-restore schema check:\n%s", r.calls)
					}
					if baseline {
						migration := strings.Index(r.calls, "cloud migrate up")
						if migration < restore || migration > postcheck {
							t.Fatalf("baseline was not migrated before validation:\n%s", r.calls)
						}
					}
					if restored != "118" {
						if !strings.Contains(r.output, "FAILED post-restore validation") || !strings.Contains(r.output, "DATABASE_CONTENT_RESTORED    = yes") || !strings.Contains(r.output, "RECOVERY_MARKING_COMPLETE    = NO") {
							t.Fatalf("missing honest partial-state report:\n%s", r.output)
						}
						if strings.Contains(r.calls, "SELECT id FROM devices") {
							t.Fatalf("marked devices after failed validation:\n%s", r.calls)
						}
					} else if !unmarked && !strings.Contains(r.calls, "SELECT id FROM devices") {
						t.Fatalf("successful marked restore omitted device marking:\n%s", r.calls)
					}
				})
			}
		}
	}
}

func TestRestoreScriptRetainsOverwriteAndPartialRestoreGuards(t *testing.T) {
	r := runRestoreScript(t, "118", "118", false, false, false, "FAKE_EXISTING=1")
	if r.err == nil || !strings.Contains(r.output, "target database already contains") || strings.Contains(r.calls, "pg_restore --clean") {
		t.Fatalf("overwrite refusal lost: %v\n%s\n%s", r.err, r.output, r.calls)
	}
	r = runRestoreScript(t, "118", "118", false, false, false, "FAKE_RESTORE_RC=1")
	if r.err == nil || !strings.Contains(r.output, "DATABASE_CONTENT_RESTORED    = PARTIAL / UNKNOWN") || strings.Contains(r.calls, "max(version_id)") {
		t.Fatalf("partial restore reporting lost: %v\n%s\n%s", r.err, r.output, r.calls)
	}
	r = runRestoreScript(t, "117", "118", true, false, false, "FAKE_MIGRATE_RC=1")
	if r.err == nil || !strings.Contains(r.output, "migration of the restored database failed") || !strings.Contains(r.output, "RECOVERY_MARKING_COMPLETE    = NO") || strings.Contains(r.calls, "max(version_id)") {
		t.Fatalf("failed migration reporting lost: %v\n%s\n%s", r.err, r.output, r.calls)
	}
}
