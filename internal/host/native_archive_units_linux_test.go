//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnpublishedArchiveFrontendAbsencePreservesForeignArtifacts(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil { //nolint:gosec // Private fixture root.
		t.Fatal(err)
	}
	host := archiveHost{home: home}
	ctx := context.Background()
	if err := host.nativeArchiveUnpublishedFrontendAbsent(ctx, "game"); err != nil {
		t.Fatal("absent namespace refused", err)
	}
	directory := filepath.Join(home, ".config", "containers", "systemd")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := host.nativeArchiveUnpublishedFrontendAbsent(ctx, "game"); err != nil {
		t.Fatal("empty namespace refused", err)
	}
	path := filepath.Join(directory, "game.container")
	if err := os.WriteFile(path, []byte("foreign frontend"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := host.nativeArchiveUnpublishedFrontendAbsent(ctx, "game"); err == nil {
		t.Fatal("foreign frontend treated as unpublished absence")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // Known owned fixture path.
	if err != nil || string(raw) != "foreign frontend" {
		t.Fatal("absence check changed foreign artifact", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(directory+"-original", directory); err != nil {
		t.Fatal(err)
	}
	if err = host.nativeArchiveUnpublishedFrontendAbsent(ctx, "game"); err == nil {
		t.Fatal("symlink namespace treated as owned absence")
	}
}

func TestNativeArchiveServiceQuiescenceAcceptsLoadedStopAndAffirmativeAbsence(t *testing.T) {
	for _, show := range []string{archiveStopped, strings.ReplaceAll(archiveStopped, "LoadState=loaded", "LoadState=not-found")} {
		host := archiveHostForTest(t, &archiveExecutor{show: show})
		stopped, err := host.stoppedOrAbsentService(context.Background(), "brine-engine-owned.service")
		if err != nil || !stopped {
			t.Fatal("known loaded stop or absence refused", err)
		}
	}
	host := archiveHostForTest(t, &archiveExecutor{show: strings.ReplaceAll(archiveStopped, "Job=", "Job=queued")})
	if stopped, err := host.stoppedOrAbsentService(context.Background(), "brine-engine-owned.service"); err == nil && stopped {
		t.Fatal("pending service job became quiescence")
	}
}
