package archive

import (
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func (d NativeDatabase) valid(p Plan, root data.ArchiveTree) bool {
	b := d.Binding
	if b.Validate() != nil || b.IncarnationID != p.Candidate.Incarnation || root.Root != b.Root || d.Head.Validate() != nil || d.Head.BindingID != b.BindingID ||
		d.State != data.EngineInitialized && d.State != data.EngineUnpublished || d.Allocation == nil || !d.Allocation.Valid() ||
		d.Allocation.DatabaseID != b.DatabaseID || d.Allocation.IncarnationID != b.IncarnationID || d.Allocation.RootDevice != root.RootDevice || d.Allocation.RootInode != root.RootInode ||
		len(d.Epochs) == 0 || len(d.Epochs) != len(d.Retention) || len(d.Dependencies) == 0 {
		return false
	}
	current := false
	for i, epoch := range d.Epochs {
		if epoch.Validate(b) != nil || i > 0 && d.Epochs[i-1].EpochID >= epoch.EpochID || !d.Retention[i].Admits(epoch.Destination, p.CreatedAt) {
			return false
		}
		current = current || epoch.EpochID == d.Head.EpochID
	}
	if !current || !nativeDependenciesValid(d) {
		return false
	}
	for i, tree := range d.TransitionTrees {
		if !data.ValidID(tree.TransitionID) || tree.Stage != data.EngineTransitionCompleted && tree.Stage != data.EngineTransitionAbandoned ||
			tree.Seal.Validate() != nil || tree.Seal.Root.Device != root.RootDevice || tree.Seal.Root.Inode != root.RootInode ||
			i > 0 && d.TransitionTrees[i-1].TransitionID >= tree.TransitionID {
			return false
		}
	}
	return true
}

func nativeDependenciesValid(d NativeDatabase) bool {
	seen := map[string]bool{}
	for _, ref := range d.Dependencies {
		if ref.DatabaseID != d.Binding.DatabaseID || ref.BindingID != d.Binding.BindingID || !data.ValidID(string(ref.EpochID)) ||
			!data.ValidID(ref.ID) || !data.ValidCatalogHash(ref.SHA256) || seen[ref.ID] || !nativeRetainedEpoch(d, ref.EpochID) {
			return false
		}
		switch ref.Kind {
		case data.EnginePreparedPointEvidence, data.EngineMaterialEvidence, data.EngineRecoveryEvidence, data.EngineDependenciesEvidence, data.EngineCheckpointEvidence:
		default:
			return false
		}
		seen[ref.ID] = true
	}
	return true
}

func nativeRetainedEpoch(d NativeDatabase, wanted data.ReplicaEpochID) bool {
	for _, epoch := range d.Epochs {
		if epoch.EpochID == wanted {
			return true
		}
	}
	return false
}

func recordNativeEpochs(d NativeDatabase, seen map[data.ReplicaEpochID]bool) bool {
	for _, epoch := range d.Epochs {
		if seen[epoch.EpochID] {
			return false
		}
		seen[epoch.EpochID] = true
	}
	return true
}

// admits validates review scope and freshness only. Protected17/21 readers join
// the actual prepared point, consumer, source/remote witness and owned tree.
func (c NativeCoverage) admits(p Plan, d NativeDatabase, now time.Time) bool {
	ref := c.Point
	if c.DatabaseID != d.Binding.DatabaseID || c.Head != d.Head || ref.Kind != data.EnginePreparedPointEvidence ||
		ref.DatabaseID != d.Binding.DatabaseID || ref.BindingID != d.Binding.BindingID || ref.EpochID != d.Head.EpochID ||
		!data.ValidID(ref.ID) || !data.ValidCatalogHash(ref.SHA256) || !data.ValidID(string(c.FenceID)) || c.FenceOwnerOpID == "" || len(c.FenceOwnerOpID) > 128 ||
		c.WritersQuiescedAt.Before(p.CreatedAt) || c.VerifiedAt.Before(c.WritersQuiescedAt) || now.Before(c.VerifiedAt) || now.Sub(c.VerifiedAt) > MaxEvidenceAge ||
		!c.ExpiresAt.After(c.VerifiedAt) || !now.Before(c.ExpiresAt) {
		return false
	}
	for _, hash := range []string{c.SourceWitnessSHA256, c.CheckpointSHA256, c.RestoreReceiptSHA256, c.StopProofSHA256} {
		if !data.ValidCatalogHash(hash) {
			return false
		}
	}
	return true
}
