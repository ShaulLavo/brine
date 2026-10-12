//go:build linux

package host

import (
	"context"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
)

type stoppedContainerRunner struct{ failure error }

func (r stoppedContainerRunner) Execute(context.Context, localexec.Command) (localexec.Result, error) {
	return localexec.Result{}, r.failure
}

func TestManagedStoppedFrontendRequiresAnActualPodmanAbsenceResult(t *testing.T) {
	for _, test := range []struct {
		failure error
		absent  bool
	}{
		{&localexec.Error{Kind: localexec.Failed, ExitCode: 1}, true},
		{&localexec.Error{Kind: localexec.NotFound, ExitCode: -1}, false},
		{&localexec.Error{Kind: localexec.Failed, ExitCode: 125}, false},
		{&localexec.Error{Kind: localexec.Timeout}, false},
	} {
		session, err := localexec.NewSession(stoppedContainerRunner{test.failure}, 1234, t.TempDir(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		name, err := managedFrontendContainerName("frontend")
		if err != nil {
			t.Fatal(err)
		}
		state, err := podman.New(session).ContainerState(context.Background(), name)
		if managedFrontendStopped(state, err) != test.absent {
			t.Fatal("lookup failure became container absence", test.failure, err)
		}
	}
}

func TestManagedRecoveryUsesTheGeneratedQuadletContainerIdentity(t *testing.T) {
	name, err := managedFrontendContainerName("frontend")
	if err != nil || name.String() != "systemd-frontend" {
		t.Fatal("managed recovery observed the wrong container", name, err)
	}
}
