//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/store"
)

func currentDescriptorSourceMatches(expected, current store.EnginePermit, artifact datapublish.Artifact) bool {
	return current.Runtime.State == data.EngineInitialized && sameEnginePermit(expected, current) && string(artifact.WasmSHA256()) == string(current.Runtime.Module.Wasm) && string(artifact.ProgramWireHash()) == string(current.Runtime.Module.Program)
}

func acquireCurrentDescriptorLease(ctx context.Context, state *store.Store, preparation *store.EnginePreparation, permit store.EnginePermit, artifact datapublish.Artifact, images *podman.Client) (*descriptorLease, error) {
	if state == nil || !preparation.Active(permit.Runtime.BindingID) {
		return nil, datapublish.ErrRecord
	}
	current, err := state.ReadEnginePermit(ctx, permit.Runtime.BindingID)
	if err != nil || !currentDescriptorSourceMatches(permit, current, artifact) {
		return nil, datapublish.ErrRecord
	}
	image, err := descriptorImage(ctx, images, current.Runtime)
	if err != nil || !preparation.Active(current.Runtime.BindingID) {
		return nil, datapublish.ErrRecord
	}
	staged, err := stagePublicationArtifact(ctx, preparation, current.Runtime.BindingID, artifact)
	if err != nil {
		return nil, err
	}
	return &descriptorLease{artifact: staged, image: image, imageRef: current.Runtime.Image}, nil
}
