//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

// nativeLocalWitness comes only from the protected reader over an independently
// owned view of the actual stopped source. Decoding evidence cannot construct it.
type nativeLocalWitness struct {
	evidence      nativeevidence.Evidence
	disposal      nativeDisposal
	checkpointSHA string
	scope         nativecapture.Scope
}

func readNativeStoppedWitness(ctx context.Context, session localexec.Session, pin nativeReaderPin, view *nativeReadView, source engineSealedCheckpoint) (nativeLocalWitness, error) {
	if source.unchanged(ctx) != nil || view == nil {
		return nativeLocalWitness{}, errEngineSource
	}
	checkpoint, scope := source.checkpoint, view.selection.Scope()
	c := checkpoint.Record().Scope
	if scope.BindingID != c.BindingID || scope.EpochID != c.EpochID || scope.ReplicaID != c.ReplicaID || scope.CaptureID != source.stopped.PointID() {
		return nativeLocalWitness{}, errEngineSource
	}
	evidence, disposal, err := readNativeView(ctx, session, pin, view, checkpoint)
	if err != nil || disposal.nonce != view.nonce || disposal.viewHash != view.seal.hash || disposal.imageID != pin.ImageID || disposal.at.IsZero() || source.unchanged(ctx) != nil {
		return nativeLocalWitness{}, errEngineSource
	}
	currentPin, err := readNativeReaderPin(ctx)
	if err != nil || currentPin != pin {
		return nativeLocalWitness{}, errEngineSource
	}
	return nativeLocalWitness{evidence: evidence, disposal: disposal, checkpointSHA: checkpoint.SHA256(), scope: scope}, nil
}

func (w nativeLocalWitness) matches(source engineSealedCheckpoint) bool {
	if source.stopped == nil || w.checkpointSHA == "" || w.checkpointSHA != source.checkpoint.SHA256() || w.disposal.at.IsZero() || w.scope.CaptureID != source.stopped.PointID() {
		return false
	}
	c := source.checkpoint.Record()
	return w.scope.BindingID == c.Scope.BindingID && w.scope.EpochID == c.Scope.EpochID && w.scope.ReplicaID == c.Scope.ReplicaID && w.evidence.DatabaseIdentity() == c.DatabaseIdentity && w.evidence.OwnerIdentity() == c.PublisherIdentity && w.evidence.Program().Wire == c.RuntimeModuleHash && w.evidence.Program().SHA256 == c.ArtifactSHA256
}
