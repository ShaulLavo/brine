//go:build linux

package host

import (
	"context"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

// Reopening retains the original completed generation. It does not mint a new
// checkpoint or infer its state from current live metadata.
func reopenNativeCheckpoint(ctx context.Context, state *store.Store, stopped *store.NativeStoppedPreparation, copied *engineSourceCopy) (engineSealedCheckpoint, error) {
	if state == nil || copied == nil || copied.sourceUnchanged(ctx) != nil {
		return engineSealedCheckpoint{}, errEngineSource
	}
	bundle, err := state.ReadNativePreparedBundle(ctx, stopped)
	checkpoint, _, initialized := bundle.Source().Initialized()
	if err != nil || !initialized || !engineSourceSealMatches(copied.original, bundle.Point().SourceSealSHA256) {
		return engineSealedCheckpoint{}, errEngineSource
	}
	permit, err := state.ReadNativeStoppedPermit(ctx, stopped)
	c := checkpoint.Record()
	runtime := permit.Runtime
	if err != nil || runtime.State != data.EngineInitialized || copied.binding != permit.Reserved.Database || permit.Allocation == nil || copied.original.root.Device != permit.Allocation.DirectoryDevice || copied.original.root.Inode != permit.Allocation.DirectoryInode || c.Scope.BindingID != string(runtime.BindingID) || c.Scope.EpochID != string(runtime.EpochID) || c.DatabaseIdentity != string(runtime.Database) || c.PublisherIdentity != string(runtime.Publisher) || c.ArtifactSHA256 != string(runtime.Module.Wasm) || c.RuntimeModuleHash != string(runtime.Module.Program) || c.SchemaSHA256 != runtime.Module.Schema.SHA256 || !slices.Equal(c.Names, runtime.Names) || !nativeCheckpointMatchesSeal(c.Members, copied.original) {
		return engineSealedCheckpoint{}, errEngineSource
	}
	source := engineSealedCheckpoint{state: state, stopped: stopped, copy: copied, original: copied.original, checkpoint: checkpoint}
	if source.unchanged(ctx) != nil {
		return engineSealedCheckpoint{}, errEngineSource
	}
	return source, nil
}

func reopenNativeEmptySource(ctx context.Context, state *store.Store, stateRoot string, stopped *store.NativeStoppedPreparation, copied *engineSourceCopy) (engineEmptySource, error) {
	fresh, err := buildNativeEmptySource(ctx, state, stateRoot, stopped, copied)
	if err != nil {
		return engineEmptySource{}, err
	}
	bundle, err := state.ReadNativePreparedBundle(ctx, stopped)
	empty, valid := bundle.Source().Empty()
	if err != nil || !valid || !engineSourceSealMatches(fresh.original, bundle.Point().SourceSealSHA256) || !nativeCheckpointMatchesSeal(empty.Members, fresh.original) || empty.PublisherReference != fresh.permit.Runtime.Secrets.Publisher.Reference || empty.SigningRecovery.Reference != fresh.permit.Runtime.Secrets.SigningPrivate.Reference || empty.SigningRecovery.Version != fresh.permit.Runtime.Secrets.SigningPrivate.Version || empty.SigningRecovery.SHA256 != fresh.permit.Runtime.Secrets.SigningPrivate.SHA256 {
		return engineEmptySource{}, errEngineSource
	}
	fresh.source = bundle.Source()
	return fresh, fresh.unchanged(ctx)
}

func nativeCheckpointMatchesSeal(members []nativecapture.CheckpointMember, seal engineTreeSeal) bool {
	matched := 0
	for _, actual := range seal.members {
		if actual.Mode&unix.S_IFMT != unix.S_IFREG || !nativeCheckpointMember(actual.Name) {
			continue
		}
		if !nativeCheckpointContains(members, actual) {
			return false
		}
		matched++
	}
	return matched == nativeCheckpointOriginalCount(members)
}

func nativeCheckpointContains(members []nativecapture.CheckpointMember, actual engineTreeMember) bool {
	for _, member := range members {
		if member.Path == actual.Name {
			return actual.Size >= 0 && uint64(actual.Size) == member.Bytes && actual.SHA256 == member.SHA256 && actual.Mode == unix.S_IFREG|0600 && actual.Links == 1
		}
	}
	return false
}

func nativeCheckpointOriginalCount(members []nativecapture.CheckpointMember) int {
	count := 0
	for _, member := range members {
		if member.Path != "recovery/signing.key" {
			count++
		}
	}
	return count
}
