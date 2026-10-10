//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type preparationPullRunner struct {
	pulled bool
	fail   bool
	calls  int
}

func (r *preparationPullRunner) Run(_ context.Context, path string, args ...string) (string, error) {
	if path == "podman" && len(args) == 4 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format={{json .RepoDigests}}" && args[3] == data.MappingProbeImage {
		if !r.pulled {
			return "", data.ErrInvalid
		}
		raw, err := json.Marshal([]string{data.MappingProbeImage})
		return string(raw), err
	}
	r.calls++
	if path != "podman" || len(args) != 2 || args[0] != "pull" || args[1] != data.MappingProbeImage {
		return "", data.ErrInvalid
	}
	if r.fail {
		return "", errors.New("fixture pull refused")
	}
	r.pulled = true
	return "pinned", nil
}
func allocationFixture(t *testing.T) (*store.Store, policy.Desired, DataFacts, DataPreparation, plan.Plan, *preparationPullRunner) {
	t.Helper()
	state, desired, facts := persistentHostFixture(t)
	proposal, err := data.NewAllocationProposal(desired.Databases[0], desired.BackupDestinations[0], data.AppIncarnationID(strings.Repeat("c", 32)))
	if err != nil {
		t.Fatal(err)
	}
	root, err := data.InspectRoot(string(desired.Databases[0].PersistentRoot))
	if err != nil {
		t.Fatal(err)
	}
	planned := plan.Plan{App: string(desired.Name), Lifecycle: plan.PrepareData, MappingImage: data.MappingProbeImage, DataAllocations: []data.AllocationProposal{proposal}, PreparationRoots: []data.RootEvidence{root}}
	runner := &preparationPullRunner{}
	preparation := DataPreparation{State: state, Runner: runner, ProbeRoot: facts.ProbeRoot, ProbeMapping: func(ctx context.Context, r localexec.Runner, root data.RootEvidence, runtime data.RuntimeIdentity) (data.MappingEvidence, error) {
		if !runner.pulled {
			return data.MappingEvidence{}, errors.New("missing mapping image")
		}
		proof, err := facts.ProbeMapping(ctx, r, root, runtime)
		proof.Image = data.MappingProbeImage
		return proof, err
	}}
	return state, desired, facts, preparation, planned, runner
}

func TestApprovedAllocationProvisionsMappingPrerequisiteBeforeProbe(t *testing.T) {
	state, desired, facts, preparation, planned, runner := allocationFixture(t)
	ctx := context.Background()
	if ready, _ := preparation.PersistentPrepared(ctx, "fixture", planned, desired); ready {
		t.Fatal("unallocated proposal claimed prepared")
	}
	if err := preparation.PreparePersistent(ctx, "fixture", planned, desired); err != nil {
		t.Fatal(err)
	}
	if !runner.pulled || runner.calls != 1 {
		t.Fatal("pinned image prerequisite not provisioned once")
	}
	ready, err := preparation.PersistentPrepared(ctx, "fixture", planned, desired)
	if !ready || err != nil {
		t.Fatalf("readback: %t %v", ready, err)
	}
	scope, err := state.ReadCredentialScopes(ctx, string(desired.Name))
	if err != nil || len(scope) != 1 {
		t.Fatalf("scope: %v %v", scope, err)
	}
	proposal := planned.DataAllocations[0]
	if scope[0].Database != proposal.Database || scope[0].Replica != proposal.Replica || scope[0].Replica.Committed {
		t.Fatal("approved IDs/scope changed or replica activated")
	}
	source := filepath.Join(string(proposal.Database.Root), proposal.Database.RelativeDirectory)
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		t.Fatalf("allocation initialized SQLite or wrote app source: %v %v", entries, err)
	}
	receipt, err := state.ReadAllocation(ctx, proposal.Database.DatabaseID)
	if err != nil || receipt == nil || receipt.RootProof == nil || receipt.MappingProof == nil {
		t.Fatalf("missing measured evidence: %+v %v", receipt, err)
	}
	facts.ProbeRoot = func(context.Context, string) (data.RootEvidence, error) {
		t.Fatal("inventory wrote a root probe")
		return data.RootEvidence{}, nil
	}
	facts.ProbeMapping = func(context.Context, localexec.Runner, data.RootEvidence, data.RuntimeIdentity) (data.MappingEvidence, error) {
		t.Fatal("inventory wrote a mapping probe")
		return data.MappingEvidence{}, nil
	}
	observed, err := facts.Collect(ctx, desired)
	if err != nil || observed.Status != target.KnownStatus || (*observed.Value)[0].Schema.State != data.AllocatedEmpty {
		t.Fatalf("approved read-only observation: %+v %v", observed, err)
	}
	runner.pulled = false
	if ready, _ := preparation.PersistentPrepared(ctx, "fixture", planned, desired); ready {
		t.Fatal("removed mapping prerequisite accepted by readback")
	}
	runner.pulled = true
	if err = state.RecordWriterAttempt(ctx, proposal.Database.IncarnationID, "fixture-attempt"); err != nil {
		t.Fatal(err)
	}
	observed, err = facts.Collect(ctx, desired)
	if err != nil || observed.Status != target.KnownStatus || (*observed.Value)[0].Schema.State != data.Unknown {
		t.Fatalf("consumed absence revived: %+v %v", observed, err)
	}
	if ready, _ := preparation.PersistentPrepared(ctx, "fixture", planned, desired); ready {
		t.Fatal("writer history accepted untouched preparation")
	}
}

func TestAllocationFailuresCreateNoReservation(t *testing.T) {
	for _, name := range []string{"pull", "mapping", "root identity", "proposal scope"} {
		t.Run(name, func(t *testing.T) {
			state, desired, _, preparation, planned, runner := allocationFixture(t)
			switch name {
			case "pull":
				runner.fail = true
			case "mapping":
				preparation.ProbeMapping = func(context.Context, localexec.Runner, data.RootEvidence, data.RuntimeIdentity) (data.MappingEvidence, error) {
					return data.MappingEvidence{}, data.ErrInvalid
				}
			case "root identity":
				planned.PreparationRoots[0].Inode++
			case "proposal scope":
				planned.DataAllocations[0].Replica.Destination.CredentialRef = "foreign"
			}
			if err := preparation.PreparePersistent(context.Background(), "fixture", planned, desired); err == nil {
				t.Fatal("unsafe preparation accepted")
			}
			if _, err := state.ActiveDataIncarnation(context.Background(), string(desired.Name)); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed preallocation reserved: %v", err)
			}
			entries, err := os.ReadDir(string(desired.Databases[0].PersistentRoot))
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed preparation changed root: %v %v", entries, err)
			}
		})
	}
}
