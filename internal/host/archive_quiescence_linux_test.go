//go:build linux

package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
)

type archiveExecutor struct {
	show      string
	listed    string
	inspected string
	stopCalls int
}

func (e *archiveExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path == "/usr/bin/systemctl" {
		if len(c.Args) > 1 && c.Args[1] == "stop" {
			e.stopCalls++
			return localexec.Result{}, nil
		}
		return localexec.Result{Stdout: e.show}, nil
	}
	if c.Path == "/usr/bin/podman" {
		if c.Args[0] == "ps" {
			return localexec.Result{Stdout: e.listed}, nil
		}
		return localexec.Result{Stdout: e.inspected}, nil
	}
	return localexec.Result{}, archive.ErrRefused
}

const archiveStopped = "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=0\nControlGroup=\nJob=\n"

func archiveHostForTest(t *testing.T, e *archiveExecutor) archiveHost {
	t.Helper()
	session, err := localexec.NewSession(e, 1000, t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return archiveHost{session: session, executor: e}
}
func TestArchiveNeverStopsAnUnpublishedService(t *testing.T) {
	for _, show := range []string{archiveStopped, strings.ReplaceAll(archiveStopped, "MainPID=0", "MainPID=100"), "garbage"} {
		e := &archiveExecutor{show: show}
		h := archiveHostForTest(t, e)
		if err := h.stopService(context.Background(), "hello.service", true); err == nil || e.stopCalls != 0 {
			t.Fatal("unowned service stopped", err, e.stopCalls)
		}
	}
}
func TestArchiveUnknownManagerObservationNeverStops(t *testing.T) {
	e := &archiveExecutor{show: strings.ReplaceAll(archiveStopped, "MainPID=0", "MainPID=unknown")}
	h := archiveHostForTest(t, e)
	if err := h.stopService(context.Background(), "hello.service", false); err == nil || e.stopCalls != 0 {
		t.Fatal("unknown manager observation acted on", err)
	}
}
func TestArchiveIndependentMountedWriterProof(t *testing.T) {
	id := strings.Repeat("a", 64)
	p := archive.Plan{Candidate: archive.Candidate{App: "hello", ReleaseID: strings.Repeat("a", 32)}, Trees: []data.ArchiveTree{{Root: "/private/data", SourceRelative: "apps/" + strings.Repeat("b", 32)}}}
	for _, test := range []struct {
		name, source string
		running      bool
		pid          int
		want         bool
	}{{"writer", "/private/data", true, 100, false}, {"orphan process", "/private/data", false, 100, false}, {"unrelated", "/unrelated", true, 100, true}, {"stopped", "/private/data", false, 0, true}, {"relative path", "relative", true, 100, false}} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal([]map[string]any{{"Id": id, "State": map[string]any{"Running": test.running, "Pid": test.pid}, "Mounts": []map[string]string{{"Source": test.source}}}})
			if err != nil {
				t.Fatal(err)
			}
			e := &archiveExecutor{show: archiveStopped, listed: id, inspected: string(raw)}
			h := archiveHostForTest(t, e)
			ok, err := h.WritersStopped(context.Background(), p)
			if ok != test.want || test.want && err != nil {
				t.Fatal("incorrect process proof", ok, err)
			}
		})
	}
}
func TestArchiveAbsenceMustBeAffirmative(t *testing.T) {
	e := &archiveExecutor{show: strings.Replace(archiveStopped, "LoadState=loaded", "LoadState=not-found", 1)}
	h := archiveHostForTest(t, e)
	if err := h.stopService(context.Background(), "hello.service", true); err != nil || e.stopCalls != 0 {
		t.Fatal("affirmative absence refused", err)
	}
}

func TestArchiveRecoveryQuiescenceAcceptsStoppedOrAbsentButNeverStops(t *testing.T) {
	p := archive.Plan{Candidate: archive.Candidate{App: "hello"}, Trees: []data.ArchiveTree{{Root: "/private/data", SourceRelative: "apps/old", ArchiveRelative: "archives/retained"}}}
	for _, show := range []string{archiveStopped, strings.ReplaceAll(archiveStopped, "loaded", "not-found"), strings.ReplaceAll(archiveStopped, "Job=", "Job=17"), strings.ReplaceAll(archiveStopped, "ControlGroup=", "ControlGroup=/user.slice/job"), "garbage"} {
		e := &archiveExecutor{show: show}
		h := archiveHostForTest(t, e)
		err := h.recoveryQuiescent(context.Background(), p)
		want := show == archiveStopped || show == strings.ReplaceAll(archiveStopped, "loaded", "not-found")
		if (err == nil) != want || e.stopCalls != 0 {
			t.Fatal("unsafe recovery process proof", show, err, e.stopCalls)
		}
	}
	id := strings.Repeat("a", 64)
	for _, source := range []string{"/private/data/apps/old", "/private/data/archives/retained"} {
		raw, err := json.Marshal([]map[string]any{{"Id": id, "State": map[string]any{"Running": true, "Pid": 101}, "Mounts": []map[string]string{{"Source": source}}}})
		if err != nil {
			t.Fatal(err)
		}
		e := &archiveExecutor{show: archiveStopped, listed: id, inspected: string(raw)}
		h := archiveHostForTest(t, e)
		if err = h.recoveryQuiescent(context.Background(), p); err == nil || e.stopCalls != 0 {
			t.Fatal("running source/archive mount admitted", source, err)
		}
	}
}
