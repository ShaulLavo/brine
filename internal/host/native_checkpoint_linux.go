//go:build linux

package host

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

type nativeOwnedObservation struct {
	permit   store.EnginePermit
	metadata spacetimedb.Observation
	at       time.Time
}

func observeOwnedNative(ctx context.Context, handle *engineNativeHandle, permit store.EnginePermit, material engineMaterial) (nativeOwnedObservation, error) {
	if handle == nil || handle.alive() != nil || permit.Runtime.State != data.EngineInitialized {
		return nativeOwnedObservation{}, errEngineSource
	}
	runtime := permit.Runtime
	expected, err := spacetimedb.NewExpectedDatabase(string(runtime.Database), string(runtime.Publisher), runtime.Names)
	if err != nil {
		return nativeOwnedObservation{}, errEngineSource
	}
	endpoint, err := spacetimedb.NewPrivateEndpoint("127.0.0.1", runtime.HostPort, string(runtime.Database))
	if err != nil {
		return nativeOwnedObservation{}, errEngineSource
	}
	credential, err := spacetimedb.NewPublisherCredential(material.publisher)
	if err != nil {
		return nativeOwnedObservation{}, errEngineSource
	}
	observed, err := spacetimedb.ObserveCurrent(ctx, endpoint, credential, expected, spacetimedb.DefaultObserverLimits())
	if err != nil || handle.alive() != nil || !nativeObservationMatches(runtime, observed) {
		return nativeOwnedObservation{}, errEngineSource
	}
	return nativeOwnedObservation{permit: permit, metadata: observed, at: time.Now().UTC()}, nil
}

func nativeObservationMatches(runtime data.EngineRuntime, observed spacetimedb.Observation) bool {
	return runtime.State == data.EngineInitialized && data.ValidCatalogHash(string(runtime.Database)) && data.ValidCatalogHash(string(runtime.Publisher)) && string(observed.Database()) == string(runtime.Database) && string(observed.Publisher()) == string(runtime.Publisher) && string(observed.CurrentModule()) == string(runtime.Module.Program) && observed.Schema().Version() == runtime.Module.Schema.Canonicalizer && observed.Schema().SHA256() == runtime.Module.Schema.SHA256 && slices.Equal(observed.Names(), runtime.Names)
}

// engineSealedCheckpoint is built from an actual stopped owned copy, protected
// runtime/material and owned prior live observation. Decoded checkpoint syntax
// cannot construct it. Its original seal is C's actual source seal.
type engineSealedCheckpoint struct {
	checkpoint nativecapture.Checkpoint
	original   engineTreeSeal
	copy       *engineSourceCopy
	stopped    *store.NativeStoppedPreparation
	state      *store.Store
}

func buildNativeCheckpoint(ctx context.Context, state *store.Store, stateRoot string, stopped *store.NativeStoppedPreparation, copied *engineSourceCopy, observed nativeOwnedObservation, replica uint64) (engineSealedCheckpoint, error) {
	if state == nil || copied == nil || copied.sourceUnchanged(ctx) != nil || observed.at.IsZero() {
		return engineSealedCheckpoint{}, errEngineSource
	}
	permit, err := state.ReadNativeStoppedPermit(ctx, stopped)
	if err != nil || !sameEnginePermit(permit, observed.permit) || permit.Runtime.State != data.EngineInitialized || !nativeObservationMatches(permit.Runtime, observed.metadata) || permit.Allocation == nil || copied.binding != permit.Reserved.Database || copied.original.root.Device != permit.Allocation.DirectoryDevice || copied.original.root.Inode != permit.Allocation.DirectoryInode {
		return engineSealedCheckpoint{}, errEngineSource
	}
	material, err := readEngineMaterial(ctx, stateRoot, permit)
	if err != nil {
		return engineSealedCheckpoint{}, err
	}
	material.clear()
	generation, err := data.NewID()
	if err != nil {
		return engineSealedCheckpoint{}, errEngineSource
	}
	var members []nativecapture.CheckpointMember
	var config string
	for _, member := range copied.original.members {
		if member.Mode&unix.S_IFMT != unix.S_IFREG || !nativeCheckpointMember(member.Name) {
			continue
		}
		if member.Size <= 0 || member.Size > 32<<20 {
			return engineSealedCheckpoint{}, errEngineSource
		}
		members = append(members, nativecapture.CheckpointMember{Path: member.Name, Bytes: uint64(member.Size), SHA256: member.SHA256})
		if member.Name == "config.toml" {
			config = member.SHA256
		}
	}
	r := permit.Runtime
	checkpoint, err := nativecapture.NewCheckpoint(nativecapture.CheckpointRecord{Format: nativecapture.Format, Scope: nativecapture.CheckpointScope{BindingID: string(r.BindingID), EpochID: string(r.EpochID), ReplicaID: replica}, GenerationID: generation, DatabaseIdentity: string(r.Database), PublisherIdentity: string(r.Publisher), Names: r.Names, RuntimeModuleHash: string(r.Module.Program), ArtifactSHA256: string(r.Module.Wasm), SchemaVersion: r.Module.Schema.Canonicalizer, SchemaSHA256: r.Module.Schema.SHA256, ConfigurationSHA256: config, SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: r.Secrets.SigningPrivate.Reference, Version: r.Secrets.SigningPrivate.Version, SHA256: r.Secrets.SigningPrivate.SHA256}, Members: members}, nativecapture.DefaultLimits())
	if err != nil || copied.sourceUnchanged(ctx) != nil {
		return engineSealedCheckpoint{}, errEngineSource
	}
	current, err := state.ReadNativeStoppedPermit(ctx, stopped)
	if err != nil || !sameEnginePermit(current, permit) {
		return engineSealedCheckpoint{}, errEngineSource
	}
	return engineSealedCheckpoint{checkpoint: checkpoint, original: copied.original, copy: copied, stopped: stopped, state: state}, nil
}

func nativeCheckpointMember(name string) bool {
	return name == "config.toml" || name == "metadata.toml" || strings.HasPrefix(name, "control-db/") || strings.HasPrefix(name, "program-bytes/")
}

func (c engineSealedCheckpoint) unchanged(ctx context.Context) error {
	if c.state == nil || c.copy == nil || c.checkpoint.SHA256() == "" || c.copy.original.hash != c.original.hash || c.copy.sourceUnchanged(ctx) != nil {
		return errEngineSource
	}
	_, err := c.state.ReadNativeStoppedPermit(ctx, c.stopped)
	return err
}
