//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/store"
)

func verifyRetainedInitialSource(ctx context.Context, stateRoot string, permit store.EnginePermit, seal engineTreeSeal, owner store.NativeInitialOwnerObservation) error {
	members, config, err := nativeEmptyMembers(seal)
	if err != nil || owner.SourceSealSHA256 != seal.hash {
		return errEngineSource
	}
	recovery, key, err := backupSigningRecovery(ctx, stateRoot, permit)
	if err != nil {
		return err
	}
	r := permit.Runtime
	source, err := nativecapture.UnpublishedSource(nativecapture.EmptyRecord{Format: nativecapture.Format, Scope: nativecapture.EmptyScope{BindingID: string(r.BindingID), EpochID: string(r.EpochID)}, GenerationID: owner.GenerationID, ConfigurationSHA256: config, PublisherReference: r.Secrets.Publisher.Reference, SigningRecovery: recovery, Members: append(members, key)}, nativecapture.DefaultLimits())
	if err != nil || source.SHA256() != owner.SourceSHA256 {
		return errEngineSource
	}
	return nil
}
