package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/fleetupdate"
	"github.com/faroukelabady/MoonLightCloud/internal/release"
)

func TestFleetSelectsWindowsFullInstallerAndLinuxBundle(t *testing.T) {
	f := newFleetFixture(t)
	ctx := context.Background()
	for _, tc := range []struct{ id, os string }{{f.devA, "windows"}, {f.devA2, "linux"}} {
		if err := f.svc.RecordStatus(ctx, tc.id, fleetupdate.StatusReport{Version: "1.0.0", BuildCommit: strings.Repeat("a", 40), ReleaseSequence: 10, OS: tc.os, Arch: "amd64", UpdaterProtocol: 1, UpdaterCapable: true, UpdateState: "idle"}); err != nil {
			t.Fatal(err)
		}
	}
	manifest := release.Manifest{ReleaseSequence: 11, Version: "1.1.0", BuildCommit: strings.Repeat("b", 40), Artifacts: []release.Artifact{
		{OS: "windows", Arch: "amd64", Package: "tar.gz", FileName: "wrong-windows.tar.gz", Size: 100, SHA256: strings.Repeat("c", 64)},
		{OS: "windows", Arch: "amd64", Package: "nsis-exe", FileName: "setup.exe", Size: 100, SHA256: strings.Repeat("d", 64)},
		{OS: "linux", Arch: "amd64", Package: "tar.gz", FileName: "linux.tar.gz", Size: 100, SHA256: strings.Repeat("e", 64)},
	}}
	envelope, _, err := release.Sign(manifest, f.priv)
	if err != nil {
		t.Fatal(err)
	}
	rel, _, err := f.svc.ImportRelease(ctx, "op", envelope, map[string]string{
		"wrong-windows.tar.gz": "https://artifacts.example.test/wrong-windows.tar.gz",
		"setup.exe":            "https://artifacts.example.test/setup.exe",
		"linux.tar.gz":         "https://artifacts.example.test/linux.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "store", StoreID: f.storeA, Mode: "optional", Percentage: 100}, true)
	for _, tc := range []struct{ id, file string }{{f.devA, "setup.exe"}, {f.devA2, "linux.tar.gz"}} {
		command := f.poll(tc.id)
		if command == nil || command.ArtifactURL != "https://artifacts.example.test/"+tc.file {
			t.Fatalf("wrong artifact for %s: %+v", tc.id, command)
		}
	}
	// A Windows device cannot be offered the old archive if NSIS is absent.
	windowsOnly := f.device("windows-no-installer", f.storeB)
	if err := f.svc.RecordStatus(ctx, windowsOnly, fleetupdate.StatusReport{Version: "1.0.0", BuildCommit: strings.Repeat("a", 40), ReleaseSequence: 10, OS: "windows", Arch: "amd64", UpdaterProtocol: 1, UpdaterCapable: true, UpdateState: "idle"}); err != nil {
		t.Fatal(err)
	}
	linuxOnly := f.release(12)
	ro := f.rollout(fleetupdate.RolloutRequest{ReleaseID: linuxOnly.ID, Scope: "store", StoreID: f.storeB, Mode: "optional", Percentage: 100}, true)
	if f.poll(windowsOnly) != nil {
		t.Fatal("Windows received a Linux-only release")
	}
	targets, _, err := f.svc.ListTargets(ctx, ro.ID, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.DeviceID == windowsOnly && target.State != fleetupdate.TargetUnsupported {
			t.Fatalf("unsupported platform reported %s", target.State)
		}
	}
}
