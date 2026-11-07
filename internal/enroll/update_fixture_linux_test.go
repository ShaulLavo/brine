//go:build linux

package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/localexec"
)

type updateFixture struct {
	h                *host
	saved            []byte
	saves            int
	failSave         int
	trace            []string
	state            admission.State
	launches         map[string]int
	settleDisables   int
	disableSawSettle bool
	effectError      bool
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{recordDir, "/usr/local/bin", "/etc/systemd/system", "/etc/systemd/user", "/etc/systemd/user-generators", "/etc/polkit-1/rules.d", "/etc/ssh/brine/authorized_keys", "/etc/ssh/sshd_config.d", "/etc/caddy", "/opt/brine/litestream/0.5.17", "/opt/brine/restic/0.19.1", "/opt/brine/native-reader/brine-native-reader-v1", "/etc/brine", "/" + admissionState, "/proc", "/var/lib/systemd/linger"} {
		if err := os.MkdirAll(filepath.Join(root, strings.TrimPrefix(path, "/")), 0700); err != nil {
			t.Fatal(err)
		}
	}
	fixture := &updateFixture{launches: map[string]int{}, state: admission.State{Version: 1, Open: true}}
	h := &host{source: []byte("candidate"), identityKey: make([]byte, 32), r: hostRecord{ID: strings.Repeat("a", 32), Key: "fixture-key", UID: os.Geteuid(), GID: os.Getegid(), BinaryHash: hash([]byte("previous")), Files: map[string]ownedFile{}, Dirs: map[string]ownedDir{}, Journal: Journal{Phase: Enrolled, Intents: map[string]bool{"binary": true}}}}
	fixture.h = h
	h.exec = updateFixtureExecutor{fixture}
	h.updateEnv = &updateEnvironment{fs: updateFS{root: root, owner: os.Geteuid()}, save: fixture.save, state: func(context.Context) (admission.State, error) { return fixture.state, nil }, drain: fixture.drain, prove: func(context.Context, AdmissionProofContext, bool) error {
		fixture.trace = append(fixture.trace, "proof")
		return nil
	}}
	return fixture
}
func (f *updateFixture) save(r hostRecord) error {
	f.saves++
	if f.failSave == f.saves {
		return errors.New("fixture crash before save")
	}
	raw, err := json.Marshal(r)
	if err == nil {
		f.saved = raw
	}
	return err
}
func (f *updateFixture) restart(t *testing.T) {
	t.Helper()
	var record hostRecord
	if err := json.Unmarshal(f.saved, &record); err != nil {
		t.Fatal(err)
	}
	f.h.r = record
	f.failSave = 0
}
func (f *updateFixture) drain(_ context.Context, p admission.Pending, fresh bool) (updateDrain, error) {
	f.trace = append(f.trace, "L2")
	if fresh && !f.state.Open {
		return nil, admission.ErrClosed
	}
	if !fresh && (f.state.Open || f.state.Pending.ID != p.ID) {
		return nil, admission.ErrInconsistent
	}
	f.state = admission.State{Version: 1, Pending: &p}
	return fixtureUpdateDrain{f}, nil
}

type fixtureUpdateDrain struct{ f *updateFixture }

func (d fixtureUpdateDrain) Close() error { return nil }
func (d fixtureUpdateDrain) SetStage(_ context.Context, s admission.Stage) error {
	d.f.trace = append(d.f.trace, "L4", string(s))
	d.f.state.Pending.Stage = s
	return nil
}
func (d fixtureUpdateDrain) RevokeAndDrain(context.Context) error {
	d.f.trace = append(d.f.trace, "revoke", "L4")
	d.f.state.Pending.ExemptOpen = false
	return nil
}
func (d fixtureUpdateDrain) EnableStage(_ context.Context, s admission.Stage) (admission.Exemption, error) {
	p := d.f.state.Pending
	p.Stage = s
	p.ExemptOpen = true
	p.ExemptAttempt = strings.Repeat("b", 32)
	return admission.Exemption{Kind: p.Kind, ID: p.ID, Stage: s, Attempt: p.ExemptAttempt}, nil
}
func (d fixtureUpdateDrain) Reopen(context.Context) error {
	d.f.trace = append(d.f.trace, "open", "release-L2")
	d.f.state = admission.State{Version: 1, Open: true}
	return nil
}

type updateFixtureExecutor struct{ f *updateFixture }

func (e updateFixtureExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	f := e.f
	if c.Path == "systemd-run" {
		if strings.Contains(strings.Join(c.Args, " "), "/usr/bin/systemctl disable brine-admission-settle.service") {
			f.settleDisables++
			observed, _, err := f.h.updateEnv.fs.read(settleUnitPath)
			f.disableSawSettle = err == nil && observed.Present && observed.SHA256 == hash([]byte(settleUnit))
		}
		unit := c.Args[2]
		f.launches[unit]++
		f.trace = append(f.trace, "issue:"+unit)
		if f.effectError {
			return localexec.Result{}, errors.New("lost launch reply")
		}
		return localexec.Result{}, nil
	}
	if c.Path == "systemctl" && len(c.Args) > 1 && c.Args[0] == "show" {
		unit := c.Args[1]
		if len(c.Args) == 4 && c.Args[2] == "--property=LoadState" && c.Args[3] == "--value" {
			return localexec.Result{Stdout: "loaded\n"}, nil
		}
		return localexec.Result{Stdout: "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\nInvocationID=abc\nMainPID=0\nJob=0\n"}, unitError(f, unit)
	}
	if c.Path == "systemctl" && c.Args[0] == "stop" {
		return localexec.Result{}, nil
	}
	if c.Path == "/usr/sbin/runuser" && strings.Contains(strings.Join(c.Args, " "), "--user show brine-reconcile.service") {
		invocation := "old"
		if f.h.r.Update != nil && f.launches[f.h.effectName("reconcile")] > 0 {
			invocation = "new"
		}
		return localexec.Result{Stdout: "LoadState=loaded\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainStatus=0\nInvocationID=" + invocation + "\nMainPID=0\nJob=0\n"}, nil
	}
	if c.Path == "/usr/sbin/runuser" && strings.Contains(strings.Join(c.Args, " "), "version --json") {
		return localexec.Result{Stdout: `{"data":{"version":"candidate"}}`}, nil
	}
	return localexec.Result{}, errors.New("unexpected fixture command: " + c.Path + " " + strings.Join(c.Args, " "))
}
func unitError(f *updateFixture, unit string) error {
	if f.launches[unit] == 0 {
		return errors.New("no recorded native issuer")
	}
	return nil
}
func (f *updateFixture) write(t *testing.T, path string, data []byte, mode uint32) UpdateFile {
	t.Helper()
	file, err := f.h.updateEnv.fs.write(path, data, mode, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	return file
}
func (f *updateFixture) pending(t *testing.T, plan UpdatePlan) {
	t.Helper()
	u, err := newUpdateRecord(plan)
	if err != nil {
		t.Fatal(err)
	}
	f.h.r.Update = u
	f.state = admission.State{Version: 1, Pending: &admission.Pending{Kind: admission.Update, ID: u.ID, Stage: admission.Fenced, Quiescence: plan.Quiescence, Since: u.Since}}
}
