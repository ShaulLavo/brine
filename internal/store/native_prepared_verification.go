package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func decodeNativePreparedVerification(v NativePreparedPointView, raw []byte) (nativePreparedVerification, error) {
	var proof nativePreparedVerification
	fields := []string{"format", "point_id", "source_sha256", "source_native_sha256", "restored_native_sha256", "final_pairs_sha256", "confirmed_bound_sha256", "manifest_sha256", "transport_sha256", "reader_nonce", "reader_view_sha256", "reader_image_id", "reader_disposed_at", "verified_at"}
	if len(raw) == 0 || len(raw) > 16384 {
		return proof, &IntegrityError{}
	}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &proof) != nil {
		return proof, &IntegrityError{}
	}
	canonical, _ := json.Marshal(proof)
	if !bytes.Equal(raw, canonical) || proof.Format != "brine-native-prepared-verification-v1" || proof.PointID != v.PointID || proof.SourceSHA256 != v.SourceSHA256 || proof.ManifestSHA256 != v.BundleManifestSHA256 || proof.TransportSHA256 != v.TransportPointSHA256 || proof.VerifiedAt != v.PreparedAt || proof.VerifiedAt.Location() != time.UTC || proof.VerifiedAt.After(time.Now().UTC()) {
		return proof, &IntegrityError{}
	}
	for _, hash := range []string{proof.SourceNativeSHA256, proof.RestoredNativeSHA256, proof.FinalPairsSHA256, proof.ConfirmedBoundSHA256} {
		if !data.ValidCatalogHash(hash) {
			return proof, &IntegrityError{}
		}
	}
	if v.SourceKind == "unpublished" {
		empty := nativePointHash(nil)
		if proof.SourceNativeSHA256 != empty || proof.RestoredNativeSHA256 != empty || proof.FinalPairsSHA256 != empty || proof.ConfirmedBoundSHA256 != empty || proof.ReaderNonce != "" || proof.ReaderViewSHA256 != "" || proof.ReaderImageID != "" || !proof.ReaderDisposedAt.IsZero() {
			return proof, &IntegrityError{}
		}
		return proof, nil
	}
	if proof.SourceNativeSHA256 != v.SourceWitnessSHA256 || !data.ValidID(proof.ReaderNonce) || !data.ValidCatalogHash(proof.ReaderViewSHA256) || !data.ValidCatalogHash(proof.ReaderImageID) || proof.ReaderDisposedAt.IsZero() || proof.ReaderDisposedAt.Location() != time.UTC || proof.ReaderDisposedAt.After(proof.VerifiedAt) {
		return proof, &IntegrityError{}
	}
	return proof, nil
}

func checkNativePreparedBundleVerification(ctx context.Context, tx *sql.Tx, point nativePreparedPoint, bundle NativePreparedBundle, sourceNative, restoredNative, confirmed, final []byte) error {
	intent, raw, err := readNativePreparationIntent(ctx, tx, point.view.PointID)
	if err != nil {
		return err
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, raw)
	if err != nil || len(events) != 6 {
		return &IntegrityError{}
	}
	proof, err := decodeNativePreparedVerification(point.view, events[4].Proof)
	if err != nil {
		return err
	}
	if proof.SourceNativeSHA256 != nativePointHash(sourceNative) || proof.RestoredNativeSHA256 != nativePointHash(restoredNative) || proof.ConfirmedBoundSHA256 != nativePointHash(confirmed) || proof.FinalPairsSHA256 != nativePointHash(final) {
		return &IntegrityError{}
	}
	if bundle.source.Kind() == "initialized" {
		var provenanceRaw []byte
		c := bundle.source
		checkpoint, _, _ := c.Initialized()
		record := checkpoint.Record()
		err = tx.QueryRowContext(ctx, "SELECT provenance FROM data_native_checkpoints WHERE binding_id=? AND epoch_id=? AND generation_id=?", record.Scope.BindingID, record.Scope.EpochID, record.GenerationID).Scan(&provenanceRaw)
		var provenance nativeCheckpointProvenance
		if err != nil || json.Unmarshal(provenanceRaw, &provenance) != nil || provenance.ReaderDisposalSHA256 != nativePointHash([]byte(proof.ReaderNonce+proof.ReaderViewSHA256+proof.ReaderImageID)) {
			return &IntegrityError{}
		}
	}
	return nil
}
