package plan

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestDeploymentReviewBindsCommittedReleaseAndAdmissionFacts(t *testing.T) {
	in := fixture(t, "one-app")
	baseline, err := DeploymentReviewHash(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*Input)
	}{
		{"committed release", func(in *Input) { in.State.Releases[0].ID = "another-release" }},
		{"target", func(in *Input) { in.Snapshot.Identity.ID = "another-host"; in.State.Target = in.Snapshot.Identity }},
		{"generation", func(in *Input) {
			in.Snapshot.Generation = target.Known(*in.Snapshot.Generation.Value + 1)
			in.State.Generation++
		}},
		{"image resolution", func(in *Input) { in.Image.Platform.Arch = "amd64" }},
		{"disk admission", func(in *Input) { in.Snapshot.FreeDiskBytes = target.Known(uint64(0)) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			next := fixture(t, "one-app")
			change.mutate(&next)
			hash, err := DeploymentReviewHash(next)
			if err != nil || hash == baseline {
				t.Fatal("decision change was not bound", hash, err)
			}
		})
	}
}
func TestDeploymentReviewExcludesOnlyExpectedDataWorkflowChanges(t *testing.T) {
	in := fixture(t, "ready-arm64")
	baseline, err := DeploymentReviewHash(in)
	if err != nil {
		t.Fatal(err)
	}
	observation := target.Observation[[]target.PersistentDatabase]{Status: target.Unknown}
	in.Snapshot.PersistentData = &observation
	in.State.DataIncarnations = []DataIncarnation{{App: string(in.Desired.Name), ID: "new-reservation"}}
	hash, err := DeploymentReviewHash(in)
	if err != nil || hash != baseline {
		t.Fatal("expected data preparation invalidated review", hash, err)
	}
}
