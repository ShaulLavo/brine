package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func readNativeLiveRestoreVerifiedTx(ctx context.Context, tx *sql.Tx, retained nativeLiveRestoreAttempt) (nativeLiveRestoreVerifiedRecord, string, error) {
	var proof nativeLiveRestoreVerifiedRecord
	var id, operation string
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT id,operation_id,canonical FROM data_native_live_restore_target_proofs WHERE operation_id=?", retained.claim.Attempt().OperationID).Scan(&id, &operation, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return proof, "", ErrNotFound
	}
	if err != nil {
		return proof, "", err
	}
	fields := []string{"format", "id", "attempt", "intent_sha256", "selected_point_sha256", "source_sha256", "checkpoint_sha256", "manifest_sha256", "transport_sha256", "source_native_sha256", "restored_native_sha256", "recovered_pairs_sha256", "dependencies_sha256", "target_tree", "reader_nonce", "reader_view_sha256", "reader_image_id", "reader_disposed_at", "verified_at"}
	if len(raw) == 0 || len(raw) > 65536 {
		return proof, "", &IntegrityError{}
	}
	if _, err = strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &proof) != nil {
		return proof, "", &IntegrityError{}
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(raw, canonical) || proof.Format != "brine-native-live-restore-verified-v1" || proof.ID != id || !data.ValidID(id) || proof.Attempt != retained.claim.Attempt() || proof.Attempt.OperationID != operation || proof.TargetTree.Validate() != nil {
		return proof, "", &IntegrityError{}
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, proof.Attempt.SelectedPointID)
	v := bundle.point.view
	if err != nil || proof.SelectedPointSHA256 != bundle.point.hash || proof.SourceSHA256 != v.SourceSHA256 || proof.CheckpointSHA256 != v.CheckpointSHA256 || proof.ManifestSHA256 != v.ManifestSHA256 || proof.TransportSHA256 != v.TransportSHA256 || proof.SourceNativeSHA256 != v.SourceEvidenceSHA256 || proof.DependenciesSHA256 != bundle.dependencies.hash {
		return proof, "", errors.Join(&IntegrityError{}, err)
	}
	if !data.ValidCatalogHash(proof.RestoredNativeSHA256) || !data.ValidCatalogHash(proof.RecoveredPairsSHA256) || !data.ValidID(proof.ReaderNonce) || !data.ValidCatalogHash(proof.ReaderViewSHA256) || !data.ValidCatalogHash(proof.ReaderImageID) || proof.ReaderDisposedAt.IsZero() || proof.VerifiedAt.Before(proof.ReaderDisposedAt) {
		return proof, "", &IntegrityError{}
	}
	initial, next := retained.claim, retained.claim
	intent, err := readNativeLiveRestorePhaseTx(ctx, tx, retained, liverestore.RestoreIntent, &initial)
	if err != nil || intent.SHA256() != proof.IntentSHA256 {
		return proof, "", errors.Join(&IntegrityError{}, err)
	}
	next, err = readNativeLiveRestorePhaseTx(ctx, tx, retained, liverestore.Verified, &intent)
	hash := nativePointHash(raw)
	if err != nil || next.ProofSHA256() != hash {
		return proof, "", errors.Join(&IntegrityError{}, err)
	}
	return proof, hash, nil
}

func readNativeLiveRestorePhaseTx(ctx context.Context, tx *sql.Tx, retained nativeLiveRestoreAttempt, stage liverestore.Stage, previous *liverestore.Event) (liverestore.Event, error) {
	var raw []byte
	var sequence uint64
	err := tx.QueryRowContext(ctx, "SELECT sequence,canonical FROM data_native_live_restore_events WHERE operation_id=? AND stage=?", retained.claim.Attempt().OperationID, stage).Scan(&sequence, &raw)
	if err != nil {
		return liverestore.Event{}, err
	}
	event, err := liverestore.DecodeEvent(retained.plan, previous, raw)
	if err != nil || event.Stage() != stage || event.Sequence() != sequence {
		return liverestore.Event{}, &IntegrityError{}
	}
	return event, nil
}
