package systemd

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestJobLauncherArgv(t *testing.T) {
	r := &recorder{}
	session, err := localexec.NewSession(r, 1234, "/home/brine/.local/state/brine", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	launcher := NewJobLauncher(session, 1234)
	id, err := ParseOperationID("01ABC_123")
	if err != nil {
		t.Fatal(err)
	}
	if err := launcher.Launch(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	want := []string{"--user", "--unit=brine-op-01ABC_123", "--collect", "--property=Type=exec", "--property=Restart=no", "--property=RuntimeMaxSec=900", "--property=TimeoutStopSec=30", "--property=MemoryMax=512M", "--property=CPUQuota=100%", "--property=TasksMax=128", "--property=UMask=0077", "--property=WorkingDirectory=/home/brine/.local/state/brine", "--property=StandardInput=null", "--property=StandardOutput=null", "--property=StandardError=null", "--property=UnsetEnvironment=LD_PRELOAD LD_LIBRARY_PATH BASH_ENV ENV", "--setenv=PATH=/usr/bin:/bin", "--setenv=LC_ALL=C", "--setenv=XDG_RUNTIME_DIR=/run/user/1234", "--setenv=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1234/bus", "--", "/usr/local/bin/brine", "host", "run-op", "01ABC_123"}
	if len(r.commands) != 1 || r.commands[0].Path != "systemd-run" || !reflect.DeepEqual(r.commands[0].Args, want) {
		t.Fatalf("commands: %#v", r.commands)
	}
	c := r.commands[0]
	if !c.Mutation || c.Timeout != time.Second || c.Dir != "/home/brine/.local/state/brine" || !reflect.DeepEqual(c.Env, []string{"XDG_RUNTIME_DIR=/run/user/1234", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1234/bus", "LC_ALL=C"}) {
		t.Fatalf("unsafe command: %#v", c)
	}
}

func TestOperationIDBoundary(t *testing.T) {
	for _, bad := range []string{"", "--property=ExecStart=/bin/sh", "../x", "x.service", "x;touch", "x\n", "x y", "x%u", "x/../y"} {
		if _, err := ParseOperationID(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	r := &recorder{}
	s, _ := localexec.NewSession(r, 1, "/home/brine/.local/state/brine", time.Second)
	if err := NewJobLauncher(s, 1234).Launch(context.Background(), OperationID{}); err == nil || len(r.commands) != 0 {
		t.Fatal("zero ID launched")
	}
}

func TestLaunchUnknownReconcilesOutsideCanceledContext(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		probeErr     error
		accepted     bool
	}{
		{"exists", "LoadState=loaded\n", nil, true},
		{"collected", "LoadState=not-found\n", nil, false},
		{"probe fails", "", &localexec.Error{Kind: localexec.Failed}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{results: []localexec.Result{{}, {Stdout: tc.output}}, errors: []error{&localexec.Error{Kind: localexec.UnknownOutcome}, tc.probeErr}}
			s, _ := localexec.NewSession(r, 1234, "/home/brine/.local/state/brine", time.Second)
			id, _ := ParseOperationID("op1")
			err := NewJobLauncher(s, 1234).Launch(context.Background(), id)
			if tc.accepted && err != nil {
				t.Fatal(err)
			}
			if !tc.accepted {
				var runtime *localexec.Error
				if !errors.As(err, &runtime) || runtime.Kind != localexec.UnknownOutcome {
					t.Fatalf("lost unknown outcome: %v", err)
				}
			}
			if len(r.commands) != 2 || r.commands[1].Mutation || !reflect.DeepEqual(r.commands[1].Args, []string{"--user", "show", "brine-op-op1.service", "--property=LoadState"}) {
				t.Fatalf("no unit probe: %#v", r.commands)
			}
		})
	}
}

func TestKnownLaunchFailureDoesNotRetry(t *testing.T) {
	r := &recorder{err: &localexec.Error{Kind: localexec.Failed}}
	s, _ := localexec.NewSession(r, 1, "/home/brine/.local/state/brine", time.Second)
	id, _ := ParseOperationID("op1")
	if err := NewJobLauncher(s, 1234).Launch(context.Background(), id); err == nil || len(r.commands) != 1 {
		t.Fatal("failure lost or launch retried")
	}
}
