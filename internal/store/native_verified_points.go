package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

// NativeVerifiedPointView describes a historical remotely verified persisted
// lower bound. It is separate from fresh exact preparation and its one consumer.
type NativeVerifiedPointView struct {
	Format                string                `json:"format"`
	PointID               string                `json:"point_id"`
	PointSHA256           string                `json:"point_sha256,omitempty"`
	OperationID           string                `json:"operation_id"`
	BindingID             data.ReplicaBindingID `json:"binding_id"`
	EpochID               data.ReplicaEpochID   `json:"epoch_id"`
	DatabaseID            data.DatabaseID       `json:"database_id"`
	IncarnationID         data.AppIncarnationID `json:"incarnation_id"`
	GenerationID          string                `json:"generation_id"`
	SourceSHA256          string                `json:"source_sha256"`
	CheckpointSHA256      string                `json:"checkpoint_sha256"`
	ManifestSHA256        string                `json:"manifest_sha256"`
	TransportSHA256       string                `json:"transport_sha256"`
	SourceEvidenceSHA256  string                `json:"source_evidence_sha256"`
	RestoreEvidenceSHA256 string                `json:"restore_evidence_sha256"`
	ConfirmedBoundSHA256  string                `json:"confirmed_bound_sha256"`
	FinalPairsSHA256      string                `json:"final_pairs_sha256"`
	PolicySHA256          string                `json:"policy_sha256"`
	DestinationSHA256     string                `json:"destination_sha256"`
	CredentialReference   string                `json:"credential_reference"`
	CredentialVersion     uint64                `json:"credential_version"`
	RecoveryReference     string                `json:"recovery_reference"`
	RecoveryVersion       uint64                `json:"recovery_version"`
	CommittedOffset       uint64                `json:"committed_offset"`
	CapturedAt            time.Time             `json:"captured_at"`
	VerifiedAt            time.Time             `json:"verified_at"`
	RetainUntil           time.Time             `json:"retain_until"`
	LaterLoss             string                `json:"later_loss"`
}

type nativeVerifiedPoint struct {
	view NativeVerifiedPointView
	raw  []byte
	hash string
}

func decodeNativeVerifiedPoint(raw []byte) (nativeVerifiedPoint, error) {
	if len(raw) == 0 || len(raw) > 65536 {
		return nativeVerifiedPoint{}, &IntegrityError{}
	}
	fields := []string{"format", "point_id", "operation_id", "binding_id", "epoch_id", "database_id", "incarnation_id", "generation_id", "source_sha256", "checkpoint_sha256", "manifest_sha256", "transport_sha256", "source_evidence_sha256", "restore_evidence_sha256", "confirmed_bound_sha256", "final_pairs_sha256", "policy_sha256", "destination_sha256", "credential_reference", "credential_version", "recovery_reference", "recovery_version", "committed_offset", "captured_at", "verified_at", "retain_until", "later_loss"}
	if _, err := strictjson.Object(raw, fields...); err != nil {
		return nativeVerifiedPoint{}, &IntegrityError{}
	}
	v, err := strictjson.Value[NativeVerifiedPointView](json.RawMessage(raw))
	canonical, marshalErr := json.Marshal(v)
	if err != nil || marshalErr != nil || !bytes.Equal(raw, canonical) || !validNativeVerifiedPoint(v) {
		return nativeVerifiedPoint{}, &IntegrityError{}
	}
	return nativeVerifiedPoint{view: v, raw: bytes.Clone(raw), hash: nativePointHash(raw)}, nil
}

func validNativeVerifiedPoint(v NativeVerifiedPointView) bool {
	if v.Format != "brine-native-verified-point-v1" || v.PointSHA256 != "" || !data.ValidID(v.PointID) || !nativePointOperationID.MatchString(v.OperationID) || !data.ValidID(string(v.BindingID)) || !data.ValidID(string(v.EpochID)) || !data.ValidID(string(v.DatabaseID)) || !data.ValidID(string(v.IncarnationID)) || !data.ValidID(v.GenerationID) || v.CredentialVersion == 0 || v.RecoveryVersion == 0 || !nativePointLogicalName.MatchString(v.CredentialReference) || !nativePointLogicalName.MatchString(v.RecoveryReference) || v.LaterLoss != "unknown" {
		return false
	}
	for _, digest := range []string{v.SourceSHA256, v.CheckpointSHA256, v.ManifestSHA256, v.TransportSHA256, v.SourceEvidenceSHA256, v.RestoreEvidenceSHA256, v.ConfirmedBoundSHA256, v.FinalPairsSHA256, v.PolicySHA256, v.DestinationSHA256} {
		if !data.ValidCatalogHash(digest) {
			return false
		}
	}
	return !v.CapturedAt.IsZero() && v.CapturedAt.Location() == time.UTC && v.VerifiedAt.Location() == time.UTC && v.RetainUntil.Location() == time.UTC && !v.VerifiedAt.Before(v.CapturedAt) && !v.RetainUntil.Before(v.VerifiedAt.Add(30*24*time.Hour))
}

func readNativeVerifiedPointTx(ctx context.Context, tx *sql.Tx, pointID string) (nativeVerifiedPoint, error) {
	if !data.ValidID(pointID) {
		return nativeVerifiedPoint{}, ErrInvalid
	}
	var operation string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT operation_id,binding_id,epoch_id,canonical FROM data_native_verified_points WHERE id=?", pointID).Scan(&operation, &binding, &epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeVerifiedPoint{}, ErrNotFound
	}
	if err != nil {
		return nativeVerifiedPoint{}, err
	}
	point, err := decodeNativeVerifiedPoint(raw)
	if err != nil || point.view.PointID != pointID || point.view.OperationID != operation || point.view.BindingID != binding || point.view.EpochID != epoch {
		return nativeVerifiedPoint{}, &IntegrityError{}
	}
	if err := checkNativeVerifiedPointBundle(ctx, tx, point); err != nil {
		return nativeVerifiedPoint{}, err
	}
	reserved, err := readReservedEngine(ctx, tx, point.view.BindingID)
	if err != nil || reserved.Database.DatabaseID != point.view.DatabaseID || reserved.Database.IncarnationID != point.view.IncarnationID {
		return nativeVerifiedPoint{}, errors.Join(&IntegrityError{}, err)
	}
	return point, nil
}

func checkNativeVerifiedPointBundle(ctx context.Context, tx *sql.Tx, point nativeVerifiedPoint) error {
	var source, manifest, transport, evidence, restored, confirmed, pairs []byte
	err := tx.QueryRowContext(ctx, "SELECT source,manifest,transport,source_evidence,restored_evidence,confirmed_bound,final_pairs FROM data_native_verified_point_bundles WHERE point_id=?", point.view.PointID).Scan(&source, &manifest, &transport, &evidence, &restored, &confirmed, &pairs)
	if err != nil {
		return errors.Join(&IntegrityError{}, err)
	}
	for _, item := range []struct {
		raw  []byte
		hash string
	}{{source, point.view.SourceSHA256}, {manifest, point.view.ManifestSHA256}, {transport, point.view.TransportSHA256}, {evidence, point.view.SourceEvidenceSHA256}, {restored, point.view.RestoreEvidenceSHA256}, {confirmed, point.view.ConfirmedBoundSHA256}, {pairs, point.view.FinalPairsSHA256}} {
		if len(item.raw) == 0 || nativePointHash(item.raw) != item.hash {
			return &IntegrityError{}
		}
	}
	if err := checkNativeVerifiedBundleRecords(point.view, source, manifest, transport); err != nil {
		return err
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", point.view.OperationID))
	if err != nil || op.Kind != ops.NativeCapture || op.State != ops.Succeeded {
		return errors.Join(ErrConflict, err)
	}
	input, err := readNativeTaskInput(ctx, tx, op)
	if err != nil || input.BindingID != point.view.BindingID {
		return errors.Join(&IntegrityError{}, err)
	}
	var outcomeRaw []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", op.ID).Scan(&outcomeRaw); err != nil {
		return errors.Join(&IntegrityError{}, err)
	}
	outcome, err := ops.DecodeTaskOutcome(op, outcomeRaw)
	if err != nil {
		return &IntegrityError{}
	}
	receipt, err := nativecapture.DecodeTaskReceipt(outcome.Receipt)
	if err != nil || receipt.PointID != point.view.PointID || receipt.PointSHA256 != point.hash || receipt.BindingID != string(point.view.BindingID) || receipt.EpochID != string(point.view.EpochID) {
		return &IntegrityError{}
	}
	return nil
}

func checkNativeVerifiedBundleRecords(v NativeVerifiedPointView, sourceRaw, manifestRaw, transportRaw []byte) error {
	source, err := nativecapture.DecodeSource(sourceRaw, nativecapture.DefaultLimits())
	checkpoint, selection, initialized := source.Initialized()
	if err != nil || !initialized || source.SHA256() != v.SourceSHA256 || checkpoint.SHA256() != v.CheckpointSHA256 || checkpoint.Record().GenerationID != v.GenerationID || selection.Scope().BindingID != string(v.BindingID) || selection.Scope().EpochID != string(v.EpochID) || selection.Scope().CaptureID != v.PointID {
		return &IntegrityError{}
	}
	manifest, err := restic.DecodeManifest(manifestRaw, restic.DefaultLimits())
	var transport restic.TransportPoint
	if err != nil || manifest.SHA256() != v.ManifestSHA256 || json.Unmarshal(transportRaw, &transport) != nil {
		return &IntegrityError{}
	}
	i, scope := transport.Identity, transport.Scope
	if i.CaptureID != v.PointID || i.BindingID != string(v.BindingID) || i.EpochID != string(v.EpochID) || i.CheckpointSHA256 != v.CheckpointSHA256 || i.ModuleSHA256 != checkpoint.Record().ArtifactSHA256 || scope.BindingID != i.BindingID || scope.EpochID != i.EpochID || scope.DestinationSHA256 != v.DestinationSHA256 || scope.CredentialVersion != v.CredentialVersion || scope.RecoveryVersion != v.RecoveryVersion || transport.ManifestSHA256 != v.ManifestSHA256 {
		return &IntegrityError{}
	}
	return nil
}

func (s *Store) ReadNativeVerifiedPoint(ctx context.Context, pointID string) (NativeVerifiedPointView, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := readNativeVerifiedPointTx(ctx, tx, pointID)
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	v := point.view
	v.PointSHA256 = point.hash
	return v, tx.Commit()
}
