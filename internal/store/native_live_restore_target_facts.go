package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/nativecapture"
)

// NativeRestoreTargetFacts selects exact retained signing versions and module
// metadata. It is a preparation description, not activation or start authority.
type NativeRestoreTargetFacts struct {
	Runtime      data.EngineRuntime
	Checkpoint   nativecapture.Checkpoint
	VerifiedTree data.EngineTreeSeal
}

func (s *Store) ReadNativeRestoreTargetFacts(ctx context.Context, capability *NativeLiveRestoreExecution) (NativeRestoreTargetFacts, error) {
	if capability == nil || capability.state != s || !capability.stopped.active(s) {
		return NativeRestoreTargetFacts{}, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return NativeRestoreTargetFacts{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeRestoreTargetFacts{}, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.Attempt().OperationID)
	if err != nil || retained.claim.Attempt() != capability.Attempt() || retained.current.Stage() != liverestore.Verified && retained.current.Stage() != liverestore.TransitionIntent {
		return NativeRestoreTargetFacts{}, errors.Join(ErrConflict, err)
	}
	if err = checkNativeLiveRestoreSourceTx(ctx, tx, capability.stopped, retained); err != nil {
		return NativeRestoreTargetFacts{}, err
	}
	facts, err := readNativeRestoreTargetFactsTx(ctx, tx, retained)
	if err != nil || !capability.stopped.active(s) {
		return NativeRestoreTargetFacts{}, errors.Join(ErrConflict, err)
	}
	return facts, tx.Commit()
}

func readNativeRestoreTargetFactsTx(ctx context.Context, tx *sql.Tx, retained nativeLiveRestoreAttempt) (NativeRestoreTargetFacts, error) {
	verified, _, err := readNativeLiveRestoreVerifiedTx(ctx, tx, retained)
	if err != nil {
		return NativeRestoreTargetFacts{}, err
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, retained.claim.Attempt().SelectedPointID)
	if err != nil {
		return NativeRestoreTargetFacts{}, err
	}
	permit, err := readNativeRestoreSourcePermitTx(ctx, tx, retained)
	if err != nil {
		return NativeRestoreTargetFacts{}, err
	}
	historical, err := readNativeRestoreHistoricalRuntimeTx(ctx, tx, bundle)
	if err != nil {
		return NativeRestoreTargetFacts{}, err
	}
	module, err := readEngineModule(ctx, tx, bundle.point.view.DatabaseID, historical.Module.Program)
	if err != nil || module != historical.Module {
		return NativeRestoreTargetFacts{}, errors.Join(&IntegrityError{}, err)
	}
	runtime := permit.Runtime.Clone()
	runtime.EpochID, runtime.Module = retained.claim.Attempt().IncomingEpochID, module
	runtime.Database, runtime.Publisher = data.NativeDatabaseIdentity(bundle.checkpoint.Record().DatabaseIdentity), data.NativePublisherIdentity(bundle.checkpoint.Record().PublisherIdentity)
	runtime.Names = slices.Clone(bundle.checkpoint.Record().Names)
	runtime.Secrets.SigningPrivate, runtime.Secrets.SigningPublic = historical.Secrets.SigningPrivate, historical.Secrets.SigningPublic
	if runtime.Validate() != nil {
		return NativeRestoreTargetFacts{}, ErrConflict
	}
	checkpointRecord := bundle.checkpoint.Record()
	checkpointRecord.Scope.EpochID, checkpointRecord.GenerationID = string(runtime.EpochID), retained.claim.Attempt().TransitionID
	checkpoint, err := nativecapture.NewCheckpoint(checkpointRecord, nativecapture.DefaultLimits())
	if err != nil {
		return NativeRestoreTargetFacts{}, errors.Join(ErrConflict, err)
	}
	return NativeRestoreTargetFacts{Runtime: runtime, Checkpoint: checkpoint, VerifiedTree: verified.TargetTree}, nil
}

func readNativeRestoreHistoricalRuntimeTx(ctx context.Context, tx *sql.Tx, bundle nativeVerifiedPointBundle) (data.EngineRuntime, error) {
	rows, err := tx.QueryContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? ORDER BY version DESC", bundle.point.view.BindingID)
	if err != nil {
		return data.EngineRuntime{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return data.EngineRuntime{}, err
		}
		runtime, err := decodeNativeRestoreRuntime(raw)
		if err != nil {
			return data.EngineRuntime{}, err
		}
		if nativeRestoreRuntimeMatches(runtime, bundle) {
			return runtime, nil
		}
	}
	return data.EngineRuntime{}, errors.Join(ErrNotFound, rows.Err())
}

func decodeNativeRestoreRuntime(raw []byte) (data.EngineRuntime, error) {
	var runtime data.EngineRuntime
	if len(raw) == 0 || len(raw) > 65536 || json.Unmarshal(raw, &runtime) != nil || runtime.Validate() != nil {
		return runtime, &IntegrityError{}
	}
	canonical, err := json.Marshal(runtime)
	if err != nil || !bytes.Equal(raw, canonical) {
		return runtime, &IntegrityError{}
	}
	return runtime, nil
}

func nativeRestoreRuntimeMatches(runtime data.EngineRuntime, bundle nativeVerifiedPointBundle) bool {
	v, c := bundle.point.view, bundle.checkpoint.Record()
	signing := runtime.Secrets.SigningPrivate
	return runtime.State == data.EngineInitialized && runtime.BindingID == v.BindingID && runtime.EpochID == v.EpochID && string(runtime.Database) == c.DatabaseIdentity && string(runtime.Publisher) == c.PublisherIdentity && string(runtime.Module.Program) == c.RuntimeModuleHash && string(runtime.Module.Wasm) == c.ArtifactSHA256 && runtime.Module.Schema.SHA256 == c.SchemaSHA256 && signing.Reference == bundle.dependencies.signingRef && signing.Version == bundle.dependencies.signingVersion && signing.SHA256 == bundle.dependencies.signingSHA256
}

// Read the retained source commit, never the possibly activated target head.
func readNativeRestoreSourcePermitTx(ctx context.Context, tx *sql.Tx, retained nativeLiveRestoreAttempt) (EnginePermit, error) {
	spec := retained.plan.Spec()
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? AND version=?", spec.Facts.Head.BindingID, spec.Facts.Head.Version).Scan(&raw); err != nil {
		return EnginePermit{}, err
	}
	runtime, err := decodeNativeRestoreRuntime(raw)
	if err != nil || nativePointHash(raw) != spec.Facts.Head.RuntimeSHA256 || runtime.EpochID != spec.Facts.Head.EpochID {
		return EnginePermit{}, errors.Join(&IntegrityError{}, err)
	}
	reserved, err := readReservedEngine(ctx, tx, runtime.BindingID)
	if err != nil {
		return EnginePermit{}, err
	}
	reserved, allocation, err := projectEngineEpoch(ctx, tx, reserved, spec.Facts.Head)
	if err != nil {
		return EnginePermit{}, err
	}
	if allocation == nil {
		initial, readErr := readInitialEngineAllocation(ctx, tx, reserved)
		if readErr != nil {
			return EnginePermit{}, readErr
		}
		allocation = &initial
	}
	return EnginePermit{Reserved: reserved, Runtime: runtime, Allocation: allocation}, nil
}
