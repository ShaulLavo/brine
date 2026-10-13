//go:build linux

package store

import (
	"bytes"
	"context"
	"testing"
)

func TestNativeUploadStoresAbsentModuleAsEmptyBlobs(t *testing.T) {
	for _, payload := range []string{"absent", "present"} {
		t.Run(payload, func(t *testing.T) { testNativeUploadBlobPayload(t, payload) })
	}
}

func TestNativePreparedBundleStoresAbsentEvidenceAsEmptyBlobs(t *testing.T) {
	for _, payload := range []string{"absent", "present"} {
		t.Run(payload, func(t *testing.T) { testNativePreparedBundleBlobPayload(t, payload) })
	}
}

func testNativePreparedBundleBlobPayload(t *testing.T, payload string) {
	t.Helper()
	s, prep, _, _ := nativePreparationFixture(t)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_native_prepared_points VALUES(?,?,?,?)", prep.PointID(), prep.intent.BindingID, prep.intent.EpochID, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	var evidence []byte
	if payload == "present" {
		evidence = []byte("verified evidence")
	}
	if err := insertNativePreparedBundle(ctx, tx, prep.PointID(), []byte("{}"), []byte("{}"), []byte("{}"), evidence, evidence, evidence, evidence); err != nil {
		t.Fatal("optional evidence failed required blob columns", err)
	}
	var source, restored, confirmed, final []byte
	var sourceType, restoredType, confirmedType, finalType string
	if err := tx.QueryRowContext(ctx, "SELECT source_evidence,restored_evidence,confirmed_bound,final_pairs,typeof(source_evidence),typeof(restored_evidence),typeof(confirmed_bound),typeof(final_pairs) FROM data_native_prepared_point_bundles WHERE point_id=?", prep.PointID()).Scan(&source, &restored, &confirmed, &final, &sourceType, &restoredType, &confirmedType, &finalType); err != nil {
		t.Fatal(err)
	}
	if sourceType != "blob" || restoredType != "blob" || confirmedType != "blob" || finalType != "blob" || !bytes.Equal(source, evidence) || !bytes.Equal(restored, evidence) || !bytes.Equal(confirmed, evidence) || !bytes.Equal(final, evidence) {
		t.Fatal("optional evidence changed bytes or stored NULL")
	}
}

func testNativeUploadBlobPayload(t *testing.T, payload string) {
	t.Helper()
	s, _, _, op := nativePreparationFixture(t)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var point string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM data_native_preparations WHERE operation_id=?", op.ID).Scan(&point); err != nil {
		t.Fatal(err)
	}
	var artifact, descriptor []byte
	if payload == "present" {
		artifact, descriptor = []byte("reviewed artifact"), []byte("reviewed descriptor")
	}
	if err := insertNativeUploadDescriptor(ctx, tx, op.ID, point, []byte("{}"), artifact, descriptor); err != nil {
		t.Fatal("optional payload failed required blob columns", err)
	}
	var gotArtifact, gotDescriptor []byte
	var artifactType, descriptorType string
	if err := tx.QueryRowContext(ctx, "SELECT artifact,descriptor,typeof(artifact),typeof(descriptor) FROM data_native_upload_descriptors WHERE operation_id=?", op.ID).Scan(&gotArtifact, &gotDescriptor, &artifactType, &descriptorType); err != nil {
		t.Fatal(err)
	}
	if artifactType != "blob" || descriptorType != "blob" || !bytes.Equal(gotArtifact, artifact) || !bytes.Equal(gotDescriptor, descriptor) {
		t.Fatal("optional payload representation changed contents or stored NULL")
	}
}
