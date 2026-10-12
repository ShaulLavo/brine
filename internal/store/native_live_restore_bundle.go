package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

// NativeLiveRestoreBundle describes the selected retained backup under a real
// execution capability. The returned data alone cannot admit a host effect.
type NativeLiveRestoreBundle struct {
	Point              NativeVerifiedPointView
	Source             nativecapture.Source
	Checkpoint         nativecapture.Checkpoint
	Selection          nativecapture.Selection
	Manifest           restic.Manifest
	Transport          restic.TransportPoint
	Schema             spacetimedb.CanonicalSchema
	SourceEvidence     nativeevidence.Evidence
	ConfirmedBound     spacetimedb.ConfirmedPairsRecord
	FinalPairs         spacetimedb.RecoveredPairs
	DependenciesSHA256 string
	SigningReference   string
	SigningVersion     uint64
	SigningSHA256      string
}

func (s *Store) ReadNativeLiveRestoreBundle(ctx context.Context, capability *NativeLiveRestoreExecution) (NativeLiveRestoreBundle, error) {
	if capability == nil || capability.state != s || !capability.stopped.active(s) {
		return NativeLiveRestoreBundle{}, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return NativeLiveRestoreBundle{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeLiveRestoreBundle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.event.Attempt().OperationID)
	if err != nil || retained.current.SHA256() != capability.event.SHA256() {
		return NativeLiveRestoreBundle{}, errors.Join(ErrConflict, err)
	}
	if err = checkNativeLiveRestoreExecutionTx(ctx, tx, capability.stopped, retained); err != nil {
		return NativeLiveRestoreBundle{}, err
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, retained.claim.Attempt().SelectedPointID)
	if err != nil {
		return NativeLiveRestoreBundle{}, err
	}
	schema, deps, err := readNativeVerifiedDependencies(ctx, tx, bundle.point, bundle.checkpoint)
	if err != nil || deps.hash != bundle.dependencies.hash || !capability.stopped.active(s) {
		return NativeLiveRestoreBundle{}, errors.Join(ErrConflict, err)
	}
	point := bundle.point.view
	point.PointSHA256 = bundle.point.hash
	result := NativeLiveRestoreBundle{Point: point, Source: bundle.source, Checkpoint: bundle.checkpoint, Selection: bundle.selection, Manifest: bundle.manifest, Transport: bundle.transport, Schema: schema, SourceEvidence: bundle.sourceEvidence, ConfirmedBound: bundle.confirmedBound, FinalPairs: bundle.finalPairs, DependenciesSHA256: deps.hash, SigningReference: deps.signingRef, SigningVersion: deps.signingVersion, SigningSHA256: deps.signingSHA256}
	return result, tx.Commit()
}
