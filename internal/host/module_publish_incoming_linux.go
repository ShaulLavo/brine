//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/store"
)

func initializePublicationIncoming(ctx context.Context, runtime nativeBackupRuntime, incoming *store.NativeIncomingRepository, transitionID string) (restic.InitializedRepository, error) {
	metadata, err := incoming.Metadata(ctx)
	if err != nil || metadata.IntentID != transitionID {
		return restic.InitializedRepository{}, errEngineSource
	}
	ctx, cancel, err := nativeTransportContext(ctx, metadata.Material.Scope)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	defer cancel()
	client, repository, err := runtime.repositoryMaterial(ctx, metadata.Material, incoming, metadata.CreationGrantSHA256)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	workspace, err := nativeBackupActivationWorkspace(runtime.stateRoot, transitionID)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	if err = runtime.state.BeginNativeIncomingInitialization(ctx, incoming); err != nil {
		return restic.InitializedRepository{}, err
	}
	initialized, err := client.Init(ctx, repository, workspace)
	if err != nil {
		return restic.InitializedRepository{}, err
	}
	if _, err = runtime.state.RecordNativeIncomingInitialized(ctx, incoming, initialized); err != nil {
		return restic.InitializedRepository{}, err
	}
	return initialized, nil
}
