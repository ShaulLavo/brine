//go:build linux

package host

import (
	"context"
	"os"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/store"
)

// nativeRestoredSource is filled only after exact remote extraction and native
// verification. A path, manifest or decoded replay cannot create the capability.
type nativeRestoredSource struct {
	state       *store.Store
	preparation *store.EnginePreparation
	permit      store.EnginePermit
	root        *os.Root
	path        string
	seal        engineTreeSeal
	limits      engineCopyLimits
	pointID     string
	pointSHA256 string
	source      nativecapture.Source
	checkpoint  nativecapture.Checkpoint
	manifest    restic.Manifest
	transport   restic.TransportPoint
	evidence    nativeevidence.Evidence
	original    *engineSealedCheckpoint
	empty       *engineEmptySource
}

func (s *nativeRestoredSource) unchanged(ctx context.Context) error {
	if s == nil || s.state == nil || s.preparation == nil || !s.preparation.Active(s.permit.Runtime.BindingID) || s.root == nil || !s.limits.valid() || s.seal.hash == "" || s.pointID == "" || s.pointSHA256 == "" || s.source.SHA256() == "" || s.transport.Identity.CaptureID != s.pointID || s.transport.ManifestSHA256 != s.manifest.SHA256() {
		return errEngineSource
	}
	if err := s.originalUnchanged(ctx); err != nil {
		return err
	}
	seal, err := sealEngineRoot(ctx, s.root, s.limits)
	if err != nil || seal.hash != s.seal.hash {
		return errEngineSource
	}
	current, err := engineOpenRoot(s.path)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = current.Close() }()
	seal, err = sealEngineRoot(ctx, current, s.limits)
	if err != nil || seal.hash != s.seal.hash || !s.preparation.Active(s.permit.Runtime.BindingID) {
		return errEngineSource
	}
	return nil
}

func (s *nativeRestoredSource) originalUnchanged(ctx context.Context) error {
	if s.original != nil && s.empty == nil {
		if s.original.unchanged(ctx) != nil {
			return errEngineSource
		}
		permit, err := s.state.ReadNativeStoppedPermit(ctx, s.original.stopped)
		if err != nil || !sameEnginePermit(s.permit, permit) {
			return errEngineSource
		}
		return nil
	}
	if s.empty != nil && s.original == nil && s.source.Kind() == "unpublished" && s.empty.unchanged(ctx) == nil && sameEnginePermit(s.empty.permit, s.permit) {
		return nil
	}
	return errEngineSource
}

type nativeVerifiedRestore struct {
	source   *nativeRestoredSource
	evidence nativeevidence.Evidence
	disposal nativeDisposal
}
