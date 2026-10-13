//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

// engineEmptySource requires an exhaustive owned source census under the
// actual stopped preparation. Unpublished runtime metadata alone is insufficient.
type engineEmptySource struct {
	source       nativecapture.Source
	original     engineTreeSeal
	copy         *engineSourceCopy
	stopped      *store.NativeStoppedPreparation
	state        *store.Store
	permit       store.EnginePermit
	initialStage *nativeInitialStageWork
}

func buildNativeEmptySource(ctx context.Context, state *store.Store, stateRoot string, stopped *store.NativeStoppedPreparation, copied *engineSourceCopy) (engineEmptySource, error) {
	if state == nil || copied == nil || copied.sourceUnchanged(ctx) != nil {
		return engineEmptySource{}, errEngineSource
	}
	permit, err := state.ReadNativeStoppedPermit(ctx, stopped)
	if err != nil || permit.Runtime.State != data.EngineUnpublished || permit.Allocation == nil || copied.binding != permit.Reserved.Database || copied.original.root.Device != permit.Allocation.DirectoryDevice || copied.original.root.Inode != permit.Allocation.DirectoryInode || !engineModuleHTTPDisabled(copied.root) {
		return engineEmptySource{}, errEngineSource
	}
	members, config, err := nativeEmptyMembers(copied.original)
	if err != nil {
		return engineEmptySource{}, err
	}
	private, err := sealEngineRoot(ctx, copied.root, copied.limits)
	if err != nil || !sameEngineContents(copied.original, private) {
		return engineEmptySource{}, errEngineSource
	}
	material, err := readEngineMaterial(ctx, stateRoot, permit)
	if err != nil {
		return engineEmptySource{}, err
	}
	material.clear()
	generation, err := data.NewID()
	if err != nil {
		return engineEmptySource{}, errEngineSource
	}
	r := permit.Runtime
	source, err := nativecapture.UnpublishedSource(nativecapture.EmptyRecord{Format: nativecapture.Format, Scope: nativecapture.EmptyScope{BindingID: string(r.BindingID), EpochID: string(r.EpochID)}, GenerationID: generation, ConfigurationSHA256: config, PublisherReference: r.Secrets.Publisher.Reference, SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: r.Secrets.SigningPrivate.Reference, Version: r.Secrets.SigningPrivate.Version, SHA256: r.Secrets.SigningPrivate.SHA256}, Members: members}, nativecapture.DefaultLimits())
	if err != nil {
		return engineEmptySource{}, errEngineSource
	}
	proof := engineEmptySource{source: source, original: copied.original, copy: copied, stopped: stopped, state: state, permit: permit}
	if proof.unchanged(ctx) != nil {
		return engineEmptySource{}, errEngineSource
	}
	return proof, nil
}

func nativeEmptyMembers(seal engineTreeSeal) ([]nativecapture.CheckpointMember, string, error) {
	members := make([]nativecapture.CheckpointMember, 0, len(seal.members))
	config := ""
	for _, member := range seal.members {
		if member.Name != "config.toml" && member.Name != "metadata.toml" || member.Mode != unix.S_IFREG|0600 || member.Links != 1 || member.Size <= 0 || member.Size > 64<<10 || !data.ValidCatalogHash(member.SHA256) {
			return nil, "", errEngineSource
		}
		members = append(members, nativecapture.CheckpointMember{Path: member.Name, Bytes: uint64(member.Size), SHA256: member.SHA256})
		if member.Name == "config.toml" {
			config = member.SHA256
		}
	}
	if config == "" || len(members) > 2 {
		return nil, "", errEngineSource
	}
	return members, config, nil
}

func (s engineEmptySource) unchanged(ctx context.Context) error {
	if s.state == nil || s.copy == nil || s.source.Kind() != "unpublished" || s.source.SHA256() == "" || s.copy.original.hash != s.original.hash || s.copy.sourceUnchanged(ctx) != nil {
		return errEngineSource
	}
	permit, err := s.state.ReadNativeStoppedPermit(ctx, s.stopped)
	if err != nil || !sameEnginePermit(permit, s.permit) {
		return errEngineSource
	}
	return nil
}
