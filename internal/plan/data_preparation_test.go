package plan

import (
	"slices"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

func preparationInput(t *testing.T) (Input, []data.AllocationProposal, []data.RootEvidence) {
	t.Helper()
	in := persistentReady(t)
	in.Image.ManifestDigest = target.Known(in.Image.Digest)
	root := (*in.Snapshot.PersistentData.Value)[0].Root
	root.POSIXLocks = false
	root.DurableRename = false
	unknown := target.Observation[[]target.PersistentDatabase]{Status: target.Unknown}
	in.Snapshot.PersistentData = &unknown
	proposal, err := data.NewAllocationProposal(in.Desired.Databases[0], in.Desired.BackupDestinations[0], "11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	return in, []data.AllocationProposal{proposal}, []data.RootEvidence{root}
}
func TestDataPreparationFreezesIDsWithoutInventingEmptySchema(t *testing.T) {
	in, proposals, roots := preparationInput(t)
	p, err := BuildDataPreparation(in, proposals, roots)
	if err != nil || p.Kind != Create {
		t.Fatalf("preparation: %+v %v", p, err)
	}
	if p.Lifecycle != PrepareData || p.MappingImage != data.MappingProbeImage || len(p.DataSchemas) != 0 || len(p.DataMounts) != 0 || len(p.DataCredentials) != 0 || len(p.Changes) != 1 || p.Changes[0].Kind != PrepareData {
		t.Fatalf("bootstrap granted deployment evidence: %+v", p)
	}
	if p.DataAllocations[0] != proposals[0] {
		t.Fatal("planned scope changed")
	}
	changed := slices.Clone(proposals)
	changed[0].Replica.Destination.CredentialRef = "foreign"
	if _, err = BuildDataPreparation(in, changed, roots); err == nil {
		t.Fatal("foreign credential scope admitted")
	}
	roots[0].ObservedAt = roots[0].ObservedAt.Add(time.Minute)
	roots[0].FreeBytes++
	again, err := BuildDataPreparation(in, proposals, roots)
	if err != nil || again.Hash != p.Hash {
		t.Fatal("reporting clock/capacity changed approved proposal")
	}
	roots[0].Inode++
	drift, err := BuildDataPreparation(in, proposals, roots)
	if err != nil || drift.Hash == p.Hash {
		t.Fatal("root identity drift omitted from plan")
	}
}
