package plan

import (
	"fmt"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

// BuildDataPreparation approves allocation only. Unallocated data has no schema
// observation, no empty receipt, and no permission to start writers or replicas.
func BuildDataPreparation(in Input, proposals []data.AllocationProposal, roots []data.RootEvidence) (Plan, error) {
	desired, err := in.Desired.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	facts, err := canonicalDecisionFacts(in.Snapshot, in.Desired.MinimumFreeDiskBytes)
	if err != nil {
		return Plan{}, err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return Plan{}, err
	}
	if in.Desired.Runtime == nil || in.Desired.Runtime.Validate() != nil || len(proposals) == 0 || len(proposals) != len(in.Desired.Databases) || len(proposals) > 16 || !validHash(in.Desired.PolicyHash) || !validHash(in.Image.Digest) || !in.Image.validManifest() || in.Image.ManifestDigest.Status != target.KnownStatus || in.Image.ManifestDigest.Value == nil {
		return Plan{}, fmt.Errorf("invalid preparation input")
	}
	p := Plan{SchemaVersion: SchemaVersion, App: string(in.Desired.Name), Target: in.Snapshot.Identity, ObservedGeneration: in.Snapshot.Generation, PolicyVersion: in.Desired.PolicyVersion, PolicyHash: in.Desired.PolicyHash, DesiredHash: hash(desired), Image: in.Image, Kind: Create, Lifecycle: PrepareData, Runtime: in.Desired.Runtime, Backup: in.Desired.Backup, MappingImage: data.MappingProbeImage, Secrets: []SecretBinding{}, Changes: []Change{{Kind: PrepareData}}, Conflicts: []Diagnostic{}}
	if err = in.Snapshot.Validate(); err != nil {
		return Plan{}, err
	}
	if in.Snapshot.Generation.Status != target.KnownStatus || in.Snapshot.Generation.Value == nil {
		p.Conflicts = append(p.Conflicts, Diagnostic{Code: UnknownFacts, Field: "generation"})
	}
	for _, release := range in.State.Releases {
		if release.App == p.App {
			p.Conflicts = append(p.Conflicts, Diagnostic{Code: ArtifactDrift, Field: "data_preparation"})
		}
	}
	seen := map[data.DatabaseName]bool{}
	ids := map[string]bool{}
	var incarnation data.AppIncarnationID
	for i, proposal := range proposals {
		declaration := in.Desired.Databases[i]
		var destination data.Destination
		for _, candidate := range in.Desired.BackupDestinations {
			if candidate.Reference == declaration.BackupDestination {
				destination = candidate
			}
		}
		if !proposal.Valid(declaration, destination) || seen[declaration.Name] || !slices.Contains(in.Desired.PersistentRoots, declaration.PersistentRoot) {
			return Plan{}, fmt.Errorf("invalid allocation proposal")
		}
		seen[declaration.Name] = true
		if incarnation == "" {
			incarnation = proposal.Database.IncarnationID
		}
		if proposal.Database.IncarnationID != incarnation {
			return Plan{}, fmt.Errorf("mixed incarnations")
		}
		for _, id := range []string{string(proposal.Database.DatabaseID), string(proposal.Replica.BindingID), string(proposal.Replica.EpochID)} {
			if ids[id] {
				return Plan{}, fmt.Errorf("duplicate preparation identity")
			}
			ids[id] = true
		}
		found := false
		for _, root := range roots {
			if root.Root != declaration.PersistentRoot {
				continue
			}
			found = true
			if root.Device == 0 || root.Inode == 0 || root.ObservedAt.IsZero() || root.FreeBytes < in.Desired.MinimumFreeDiskBytes || (root.Filesystem != "btrfs" && root.FreeInodes == 0) || (root.Filesystem != "ext4" && root.Filesystem != "xfs" && root.Filesystem != "btrfs") {
				p.Conflicts = append(p.Conflicts, Diagnostic{Code: InsufficientDisk, Field: "data_preparation.root"})
			}
		}
		if !found {
			p.Conflicts = append(p.Conflicts, Diagnostic{Code: UnknownFacts, Field: "data_preparation.root"})
		}
	}
	p.DataAllocations = slices.Clone(proposals)
	p.PreparationRoots = slices.Clone(roots)
	for i := range p.PreparationRoots {
		p.PreparationRoots[i].ObservedAt = time.Time{}
		p.PreparationRoots[i].FreeBytes = in.Desired.MinimumFreeDiskBytes
		p.PreparationRoots[i].FreeInodes = 0
		p.PreparationRoots[i].OperatorQuota = nil
	}
	return finish(p, desired, facts, state)
}
