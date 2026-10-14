//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"os"
	"reflect"
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

func TestManagedFrontendPendingChecksResolverRatherThanOriginalAndCurrentPolicy(t *testing.T) {
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	loader := &fakePolicy{p: pol}
	resolution := store.ManagedFrontendResolution{Pending: true, PendingOperationID: "claimed-resolver", Permit: store.ManagedFrontendPermit{OperationID: "failed-original"}, Plan: plan.Plan{PolicyHash: pol.Hash()}}
	calls := []string{}
	active := func(_ context.Context, id string) (bool, error) {
		calls = append(calls, id)
		return id == "claimed-resolver", nil
	}
	if err := verifyManagedFrontendPending(context.Background(), resolution, loader, active); err != nil || !reflect.DeepEqual(calls, []string{"claimed-resolver"}) {
		t.Fatal("pending guard used original receipt", err, calls)
	}
	resolution.PendingOperationID = ""
	if err := verifyManagedFrontendPending(context.Background(), resolution, loader, active); err == nil {
		t.Fatal("missing resolver fell back to original")
	}
	resolution.PendingOperationID = "failed-original"
	if err := verifyManagedFrontendPending(context.Background(), resolution, loader, active); err == nil {
		t.Fatal("inactive original supplied live authority")
	}
	resolution.PendingOperationID = "claimed-resolver"
	resolution.Plan.PolicyHash = "changed"
	before := len(calls)
	if err := verifyManagedFrontendPending(context.Background(), resolution, loader, active); err == nil || len(calls) != before {
		t.Fatal("stale policy reached attempt guard")
	}
	resolution.Plan.PolicyHash = pol.Hash()
	if err := verifyManagedFrontendPending(context.Background(), resolution, loader, func(context.Context, string) (bool, error) { return false, nil }); err == nil {
		t.Fatal("ended resolver retained a pending grant")
	}
}
