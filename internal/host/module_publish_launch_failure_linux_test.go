//go:build linux

package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
)

type initialLaunchFailureExecutor struct {
	result localexec.Result
	err    error
	calls  int
}

func (e *initialLaunchFailureExecutor) Execute(context.Context, localexec.Command) (localexec.Result, error) {
	e.calls++
	return e.result, e.err
}

func TestInitialPublisherLaunchRetainsTypedPrimaryFailure(t *testing.T) {
	cases := []struct {
		name, code string
		result     localexec.Result
		err        error
		kind       localexec.ErrorKind
	}{
		{name: "timeout", code: "container_start_unknown", err: context.DeadlineExceeded, kind: localexec.UnknownOutcome},
		{name: "signal", code: "container_start_unknown", err: &localexec.Error{Kind: localexec.UnknownOutcome}},
		{name: "exit", code: "container_start_failed", result: localexec.Result{ExitCode: 125, Stderr: "private path and diagnostic"}, err: &localexec.Error{Kind: localexec.Failed, ExitCode: 125}},
		{name: "truncated", code: "container_start_unknown", result: localexec.Result{Truncated: true}},
		{name: "invalid output", code: "container_start_output", result: localexec.Result{Stdout: "private non-container output"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testInitialPublisherLaunchFailure(t, tc.code, tc.result, tc.err, tc.kind)
		})
	}
}

func testInitialPublisherLaunchFailure(t *testing.T, code string, output localexec.Result, commandErr error, kind localexec.ErrorKind) {
	t.Helper()
	permit, material := engineStartupFixture(t)
	lease := &publicationStageLease{permit: permit, material: material, nonce: strings.Repeat("a", 32), stagePath: "/private/stage", initialStage: &nativeInitialStageWork{failureCode: "container_start"}}
	executor := &initialLaunchFailureExecutor{result: output, err: commandErr}
	session, err := localexec.NewSession(executor, nativeBackupFixtureUID(t), t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id, handle, err := startPublicationStageProcess(context.Background(), lease, session)
	if !errors.Is(err, datapublish.ErrRecord) || id != "" || handle != nil || executor.calls != 1 {
		t.Fatal("failed launch acquired authority or was repeated")
	}
	if kind != "" {
		var typed *localexec.Error
		if !errors.As(err, &typed) || typed.Kind != kind {
			t.Fatal("launch lost typed command failure")
		}
	}
	primary := initialPublisherFailureCode(lease, err, nil)
	lease.initialStage.failureCode = "helper_stop"
	setInitialPublisherFailure(lease, err, datapublish.ErrRecord, primary)
	if lease.initialStage.failureCode != code {
		t.Fatal("cleanup replaced primary launch diagnosis", lease.initialStage.failureCode)
	}
}

func TestInitialPublisherCleanupDiagnosisWithoutPrimaryFailure(t *testing.T) {
	lease := &publicationStageLease{initialStage: &nativeInitialStageWork{failureCode: "input_attach"}}
	setInitialPublisherFailure(lease, nil, datapublish.ErrRecord, "")
	if lease.initialStage.failureCode != "helper_disposal" {
		t.Fatal("cleanup failure lost diagnosis")
	}
	setInitialPublisherFailure(lease, nil, nil, "")
	if lease.initialStage.failureCode != "source_changed" {
		t.Fatal("changed source lost diagnosis")
	}
}
