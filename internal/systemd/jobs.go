package systemd

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

const JobWorkingDirectory = "/home/brine/.local/state/brine"
const JobRuntimeLimit = 15 * time.Minute
const LaunchProbeTimeout = 5 * time.Second

type OperationID struct{ value string }

func (id OperationID) String() string { return id.value }

var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func ParseOperationID(raw string) (OperationID, error) {
	if !operationIDPattern.MatchString(raw) {
		return OperationID{}, &localexec.Error{Kind: localexec.Invalid}
	}
	return OperationID{raw}, nil
}

type JobLauncher struct {
	session localexec.Session
	runtime string
}

func NewJobLauncher(session localexec.Session, uid uint32) *JobLauncher {
	return &JobLauncher{session: session, runtime: "/run/user/" + strconv.FormatUint(uint64(uid), 10)}
}

// Launch returns after the manager accepts the unit, not after deployment.
// A missing collected unit cannot disprove a timed-out launch; the journal wins.
func (l *JobLauncher) Launch(ctx context.Context, id OperationID) error {
	if id.value == "" {
		return &localexec.Error{Kind: localexec.Invalid}
	}
	args := []string{"--user", "--unit=brine-op-" + id.value, "--collect"}
	for _, property := range []string{
		"Type=exec", "Restart=no", "RuntimeMaxSec=900", "TimeoutStopSec=30",
		"MemoryMax=512M", "CPUQuota=100%", "TasksMax=128", "UMask=0077",
		"WorkingDirectory=" + JobWorkingDirectory, "StandardInput=null", "StandardOutput=null", "StandardError=null",
		"UnsetEnvironment=LD_PRELOAD LD_LIBRARY_PATH BASH_ENV ENV",
	} {
		args = append(args, "--property="+property)
	}
	args = append(args, "--setenv=PATH=/usr/bin:/bin", "--setenv=LC_ALL=C", "--setenv=XDG_RUNTIME_DIR="+l.runtime, "--setenv=DBUS_SESSION_BUS_ADDRESS=unix:path="+l.runtime+"/bus", "--", "/usr/local/bin/brine", "host", "run-op", id.value)
	_, err := l.session.Execute(ctx, "systemd-run", args, nil, true)
	if err == nil {
		return nil
	}
	var runtime *localexec.Error
	if !errors.As(err, &runtime) || (runtime.Kind != localexec.UnknownOutcome && runtime.Kind != localexec.Timeout) {
		return err
	}
	// SSH cancellation must not cancel the bounded reconciliation probe.
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), LaunchProbeTimeout)
	defer cancel()
	out, probeErr := l.session.Execute(probe, "systemctl", []string{"--user", "show", "brine-op-" + id.value + ".service", "--property=LoadState"}, nil, false)
	if probeErr == nil && strings.TrimSpace(out.Stdout) == "LoadState=loaded" {
		return nil
	}
	return &localexec.Error{Kind: localexec.UnknownOutcome}
}
