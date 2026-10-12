//go:build linux

package host

import (
	"context"
	"path/filepath"
	"strconv"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/store"
)

func backupSigningRecovery(ctx context.Context, stateRoot string, permit store.EnginePermit) (nativecapture.SigningRecovery, nativecapture.CheckpointMember, error) {
	key := permit.Runtime.Secrets.SigningPrivate
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		return nativecapture.SigningRecovery{}, nativecapture.CheckpointMember{}, err
	}
	defer func() { _ = root.Close() }()
	raw, err := readEnginePrivateFile(ctx, root, filepath.Join("engine-secrets", string(permit.Runtime.BindingID), "signing-private", "v"+strconv.FormatUint(key.Version, 10)+".secret"), key.SHA256, 32<<10)
	defer clear(raw)
	if err != nil || len(raw) == 0 {
		return nativecapture.SigningRecovery{}, nativecapture.CheckpointMember{}, errEngineSource
	}
	return nativecapture.SigningRecovery{Kind: "encrypted_member", Reference: key.Reference, Version: key.Version, SHA256: key.SHA256, Member: "recovery/signing.key"}, nativecapture.CheckpointMember{Path: "recovery/signing.key", Bytes: uint64(len(raw)), SHA256: key.SHA256}, nil
}

func (w *nativePrepareWork) sealedRecoveryCheckpoint(ctx context.Context, checkpoint engineSealedCheckpoint) (engineSealedCheckpoint, error) {
	recovery, member, err := backupSigningRecovery(ctx, w.runtime.stateRoot, w.permit)
	if err != nil {
		return engineSealedCheckpoint{}, err
	}
	record := checkpoint.checkpoint.Record()
	record.SigningRecovery = recovery
	record.Members = append(record.Members, member)
	checkpoint.checkpoint, err = nativecapture.NewCheckpoint(record, nativecapture.DefaultLimits())
	return checkpoint, err
}

func (w *nativePrepareWork) sealedRecoveryEmpty(ctx context.Context, empty engineEmptySource) (engineEmptySource, error) {
	recovery, member, err := backupSigningRecovery(ctx, w.runtime.stateRoot, w.permit)
	if err != nil {
		return engineEmptySource{}, err
	}
	record, valid := empty.source.Empty()
	if !valid {
		return engineEmptySource{}, errEngineSource
	}
	record.SigningRecovery = recovery
	record.Members = append(record.Members, member)
	empty.source, err = nativecapture.UnpublishedSource(record, nativecapture.DefaultLimits())
	return empty, err
}
