//go:build linux

package host

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"golang.org/x/sys/unix"
)

type publicationTargetCheckpoint struct {
	stage      publicationStageEvidence
	seal       engineTreeSeal
	checkpoint nativecapture.Checkpoint
	selection  nativecapture.Selection
}

func sealPublicationTarget(ctx context.Context, stage publicationStageEvidence) (publicationTargetCheckpoint, error) {
	lease := stage.lease
	if lease == nil || lease.working == nil || lease.working.unchanged(ctx) != nil || !lease.attempted || !lease.disposed || lease.process != nil || lease.containerID != "" || stage.source != lease.restored || stage.scope != lease.working.scope || !stage.observation.PublicationKnown || !stage.observation.ChildBound || !stage.observation.ChildStopped {
		return publicationTargetCheckpoint{}, datapublish.ErrRecord
	}
	cursor, err := stage.source.state.ReadPublicationAttemptCursor(ctx, lease.working.attempt)
	step, ready := cursor.NextStep()
	if err != nil || !ready || step != datapublish.VerifyStage || cursor.NativeDatabase() != stage.observation.Database || cursor.NativePublisher() != stage.observation.Publisher {
		return publicationTargetCheckpoint{}, datapublish.ErrRecord
	}
	review, err := stage.source.state.ReadPublicationAttemptReview(ctx, lease.working.attempt)
	if err != nil {
		return publicationTargetCheckpoint{}, err
	}
	sealed, err := sealEngineRoot(ctx, stage.source.root, stage.source.limits)
	if err != nil {
		return publicationTargetCheckpoint{}, err
	}
	generation, err := data.NewID()
	if err != nil {
		return publicationTargetCheckpoint{}, err
	}
	inventory, err := nativeOwnedInventory(ctx, stage.source.root, sealed, stage.scope.BindingID, stage.scope.NewEpochID, generation)
	if err != nil {
		return publicationTargetCheckpoint{}, err
	}
	selection, err := nativecapture.Select(inventory, nativecapture.DefaultLimits())
	if err != nil {
		return publicationTargetCheckpoint{}, err
	}
	members, configuration, err := publicationCheckpointMembers(sealed)
	if err != nil {
		return publicationTargetCheckpoint{}, err
	}
	names := slices.Clone(lease.permit.Runtime.Names)
	if review.Plan().Source().Kind() == datapublish.CreateSource {
		names = []string{review.Plan().CreationName()}
	}
	runtime := lease.permit.Runtime
	checkpoint, err := nativecapture.NewCheckpoint(nativecapture.CheckpointRecord{Format: nativecapture.Format, Scope: nativecapture.CheckpointScope{BindingID: stage.scope.BindingID, EpochID: stage.scope.NewEpochID, ReplicaID: selection.Scope().ReplicaID}, GenerationID: generation,
		DatabaseIdentity: stage.observation.Database, PublisherIdentity: stage.observation.Publisher, Names: names, RuntimeModuleHash: stage.scope.CandidateProgramWireHash, ArtifactSHA256: stage.scope.CandidateWasmSHA256, SchemaVersion: review.Plan().Candidate().Schema().Version(), SchemaSHA256: stage.scope.CandidateSchemaSHA256, ConfigurationSHA256: configuration,
		SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: runtime.Secrets.SigningPrivate.Reference, Version: runtime.Secrets.SigningPrivate.Version, SHA256: runtime.Secrets.SigningPrivate.SHA256}, Members: members}, nativecapture.DefaultLimits())
	if err != nil || lease.working.unchanged(ctx) != nil {
		return publicationTargetCheckpoint{}, datapublish.ErrRecord
	}
	return publicationTargetCheckpoint{stage: stage, seal: sealed, checkpoint: checkpoint, selection: selection}, nil
}

func publicationCheckpointMembers(seal engineTreeSeal) ([]nativecapture.CheckpointMember, string, error) {
	var members []nativecapture.CheckpointMember
	configuration := ""
	for _, member := range seal.members {
		if member.Mode&unix.S_IFMT != unix.S_IFREG || !nativeCheckpointMember(member.Name) {
			continue
		}
		if member.Size <= 0 || member.Size > 32<<20 || member.Links != 1 {
			return nil, "", datapublish.ErrRecord
		}
		members = append(members, nativecapture.CheckpointMember{Path: member.Name, Bytes: uint64(member.Size), SHA256: member.SHA256})
		if member.Name == "config.toml" {
			configuration = member.SHA256
		}
	}
	if configuration == "" {
		return nil, "", datapublish.ErrRecord
	}
	return members, configuration, nil
}

func (c publicationTargetCheckpoint) unchanged(ctx context.Context) error {
	if c.stage.lease == nil || c.stage.lease.working == nil || c.stage.lease.working.unchanged(ctx) != nil || c.checkpoint.SHA256() == "" {
		return datapublish.ErrRecord
	}
	actual, err := sealEngineRoot(ctx, c.stage.source.root, c.stage.source.limits)
	if err != nil || actual.hash != c.seal.hash {
		return datapublish.ErrRecord
	}
	return nil
}

func (c publicationTargetCheckpoint) readView(ctx context.Context) (*nativeReadView, error) {
	if c.unchanged(ctx) != nil {
		return nil, datapublish.ErrRecord
	}
	nonce, err := data.NewID()
	if err != nil {
		return nil, err
	}
	return materializeNativeSelection(ctx, c.stage.source.root, filepath.Join(c.stage.source.preparation.StateRoot(), "staging"), c.selection, nonce)
}
