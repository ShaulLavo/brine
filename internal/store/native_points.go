package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const nativePreparedPointFormat = "brine-native-prepared-point-v1"

var nativePointOperationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var nativePointLogicalName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// NativePreparedPointView contains review facts only. There is no public point
// constructor or save method. Only completed guarded preparation creates one.
type NativePreparedPointView struct {
	Format                 string                `json:"format"`
	PointID                string                `json:"point_id"`
	PointSHA256            string                `json:"point_sha256,omitempty"`
	PreparationOperationID string                `json:"preparation_operation_id"`
	FenceOwnerOpID         string                `json:"fence_owner_operation_id"`
	FenceID                data.FenceID          `json:"fence_id"`
	BindingID              data.ReplicaBindingID `json:"binding_id"`
	EpochID                data.ReplicaEpochID   `json:"epoch_id"`
	DatabaseID             data.DatabaseID       `json:"database_id"`
	IncarnationID          data.AppIncarnationID `json:"incarnation_id"`
	Generation             uint64                `json:"generation"`
	GenerationID           string                `json:"generation_id"`
	PolicySHA256           string                `json:"policy_sha256"`
	RuntimeVersion         uint64                `json:"runtime_version"`
	RuntimeSHA256          string                `json:"runtime_sha256"`
	AllocationID           string                `json:"allocation_id"`
	AllocationSHA256       string                `json:"allocation_sha256"`
	SourceKind             string                `json:"source_kind"`
	SourceSHA256           string                `json:"source_sha256"`
	SourceSealSHA256       string                `json:"source_seal_sha256"`
	SourceWitnessSHA256    string                `json:"source_witness_sha256"`
	CheckpointSHA256       string                `json:"checkpoint_sha256"`
	StopProofSHA256        string                `json:"stop_proof_sha256"`
	RestoreReceiptSHA256   string                `json:"restore_receipt_sha256"`
	BundleManifestSHA256   string                `json:"bundle_manifest_sha256"`
	TransportPointSHA256   string                `json:"transport_point_sha256"`
	ConfigSHA256           string                `json:"config_sha256"`
	SigningRecoverySHA256  string                `json:"signing_recovery_sha256"`
	NativeDatabase         string                `json:"native_database"`
	NativePublisher        string                `json:"native_publisher"`
	ProgramWireHash        string                `json:"program_wire_hash"`
	ArtifactWasmSHA256     string                `json:"artifact_wasm_sha256"`
	SchemaSHA256           string                `json:"schema_sha256"`
	Names                  []string              `json:"names"`
	PreparedAt             time.Time             `json:"prepared_at"`
	ExpiresAt              time.Time             `json:"expires_at"`
	RetainUntil            time.Time             `json:"retain_until"`
}

type nativePreparedPoint struct {
	view NativePreparedPointView
	raw  []byte
	hash string
}

func nativePointHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validNativePreparedPoint(v NativePreparedPointView) bool {
	if v.Format != nativePreparedPointFormat || v.PointSHA256 != "" || !data.ValidID(v.PointID) || !nativePointOperationID.MatchString(v.PreparationOperationID) || v.FenceOwnerOpID != v.PreparationOperationID || !data.ValidID(string(v.FenceID)) || !data.ValidID(string(v.BindingID)) || !data.ValidID(string(v.EpochID)) || !data.ValidID(string(v.DatabaseID)) || !data.ValidID(string(v.IncarnationID)) || !data.ValidID(v.GenerationID) || !data.ValidID(v.AllocationID) || v.RuntimeVersion == 0 || v.Generation > 1<<63-1 || v.RuntimeVersion > 1<<63-1 {
		return false
	}
	for _, digest := range []string{v.PolicySHA256, v.RuntimeSHA256, v.AllocationSHA256, v.SourceSHA256, v.SourceSealSHA256, v.SourceWitnessSHA256, v.CheckpointSHA256, v.StopProofSHA256, v.RestoreReceiptSHA256, v.BundleManifestSHA256, v.TransportPointSHA256, v.ConfigSHA256, v.SigningRecoverySHA256} {
		if !data.ValidCatalogHash(digest) {
			return false
		}
	}
	if v.PreparedAt.IsZero() || v.PreparedAt.Location() != time.UTC || v.ExpiresAt.Location() != time.UTC || v.RetainUntil.Location() != time.UTC || !v.ExpiresAt.After(v.PreparedAt) || v.ExpiresAt.Sub(v.PreparedAt) > 15*time.Minute || v.RetainUntil.Before(v.PreparedAt.Add(30*24*time.Hour)) || v.Names == nil {
		return false
	}
	if v.SourceKind == "unpublished" {
		return v.NativeDatabase == "" && v.NativePublisher == "" && v.ProgramWireHash == "" && v.ArtifactWasmSHA256 == "" && v.SchemaSHA256 == "" && len(v.Names) == 0
	}
	if v.SourceKind != "initialized" || !data.ValidCatalogHash(v.NativeDatabase) || !data.ValidCatalogHash(v.NativePublisher) || !data.ValidCatalogHash(v.ProgramWireHash) || !spacetimedb.SupportsConfirmedRecoveryPairs(v.ArtifactWasmSHA256, v.SchemaSHA256) || len(v.Names) == 0 || len(v.Names) > 16 || !slices.IsSorted(v.Names) {
		return false
	}
	for i, name := range v.Names {
		if !nativePointLogicalName.MatchString(name) || i > 0 && name == v.Names[i-1] {
			return false
		}
	}
	return true
}

func decodeNativePreparedPoint(raw []byte) (nativePreparedPoint, error) {
	if len(raw) == 0 || len(raw) > 65536 {
		return nativePreparedPoint{}, &IntegrityError{}
	}
	fields := []string{"format", "point_id", "preparation_operation_id", "fence_owner_operation_id", "fence_id", "binding_id", "epoch_id", "database_id", "incarnation_id", "generation", "generation_id", "policy_sha256", "runtime_version", "runtime_sha256", "allocation_id", "allocation_sha256", "source_kind", "source_sha256", "source_seal_sha256", "source_witness_sha256", "checkpoint_sha256", "stop_proof_sha256", "restore_receipt_sha256", "bundle_manifest_sha256", "transport_point_sha256", "config_sha256", "signing_recovery_sha256", "native_database", "native_publisher", "program_wire_hash", "artifact_wasm_sha256", "schema_sha256", "names", "prepared_at", "expires_at", "retain_until"}
	if _, err := strictjson.Object(raw, fields...); err != nil {
		return nativePreparedPoint{}, &IntegrityError{}
	}
	v, err := strictjson.Value[NativePreparedPointView](json.RawMessage(raw))
	canonical, marshalErr := json.Marshal(v)
	if err != nil || marshalErr != nil || !validNativePreparedPoint(v) || !bytes.Equal(canonical, raw) {
		return nativePreparedPoint{}, &IntegrityError{}
	}
	return nativePreparedPoint{view: v, raw: bytes.Clone(raw), hash: nativePointHash(raw)}, nil
}

func readNativePreparedPointTx(ctx context.Context, tx *sql.Tx, pointID string) (nativePreparedPoint, error) {
	if !data.ValidID(pointID) {
		return nativePreparedPoint{}, ErrInvalid
	}
	var raw []byte
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	err := tx.QueryRowContext(ctx, "SELECT binding_id,epoch_id,canonical FROM data_native_prepared_points WHERE id=?", pointID).Scan(&binding, &epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nativePreparedPoint{}, ErrNotFound
	}
	if err != nil {
		return nativePreparedPoint{}, err
	}
	point, err := decodeNativePreparedPoint(raw)
	if err != nil || point.view.PointID != pointID || point.view.BindingID != binding || point.view.EpochID != epoch {
		return nativePreparedPoint{}, &IntegrityError{}
	}
	if err := checkNativePointCompletion(ctx, tx, point); err != nil {
		return nativePreparedPoint{}, err
	}
	return point, nil
}

func (s *Store) ReadNativePreparedPoint(ctx context.Context, pointID string) (NativePreparedPointView, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativePreparedPointView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil {
		return NativePreparedPointView{}, err
	}
	v := point.view
	v.PointSHA256, v.Names = point.hash, slices.Clone(v.Names)
	return v, tx.Commit()
}

type nativePointConsumerKind string

const (
	nativePointModulePublish nativePointConsumerKind = "module_publish"
	nativePointLiveRestore   nativePointConsumerKind = "live_restore"
	nativePointRetire        nativePointConsumerKind = "retire"
)

type nativePointConsumer struct {
	kind             nativePointConsumerKind
	pointID          string
	planID           string
	applyOperationID string
	requester        string
	binding          data.ReplicaBindingID
	epoch            data.ReplicaEpochID
	generation       uint64
	generationID     string
	policySHA256     string
}

func (c nativePointConsumer) canonical() ([]byte, error) {
	return json.Marshal(struct {
		Kind             nativePointConsumerKind `json:"kind"`
		PointID          string                  `json:"point_id"`
		PlanID           string                  `json:"plan_id"`
		ApplyOperationID string                  `json:"apply_operation_id"`
		Requester        string                  `json:"requester"`
		Binding          data.ReplicaBindingID   `json:"binding_id"`
		Epoch            data.ReplicaEpochID     `json:"epoch_id"`
		Generation       uint64                  `json:"generation"`
		GenerationID     string                  `json:"generation_id"`
		PolicySHA256     string                  `json:"policy_sha256"`
	}{c.kind, c.pointID, c.planID, c.applyOperationID, c.requester, c.binding, c.epoch, c.generation, c.generationID, c.policySHA256})
}

// The caller derives c through its protected canonical plan reader in this tx.
// This private join independently checks operation, head, point and owner. No
// exported entry point accepts a consumer, owner override or evidence body.
func (s *Store) claimNativePreparedPointTx(ctx context.Context, tx *sql.Tx, guard *data.EngineLock, c nativePointConsumer) (nativePreparedPoint, error) {
	if !guard.HoldsLaunch(c.binding) {
		return nativePreparedPoint{}, ErrConflict
	}
	point, err := readNativePreparedPointTx(ctx, tx, c.pointID)
	if err != nil {
		return nativePreparedPoint{}, err
	}
	raw, err := c.canonical()
	if err != nil {
		return nativePreparedPoint{}, ErrInvalid
	}
	old, err := readNativePointConsumer(ctx, tx, c.pointID)
	if err == nil {
		if !bytes.Equal(old, raw) {
			return nativePreparedPoint{}, ErrConflict
		}
		return point, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nativePreparedPoint{}, err
	}
	if err = checkNativePointConsumer(ctx, tx, point, c); err != nil {
		return nativePreparedPoint{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_point_consumers VALUES(?,?,?,?,?,?,?,?,?,?,?)", c.pointID, c.kind, c.planID, c.applyOperationID, c.requester, c.binding, c.epoch, c.generation, c.generationID, c.policySHA256, raw)
	return point, err
}

func readNativePointConsumer(ctx context.Context, tx *sql.Tx, pointID string) ([]byte, error) {
	var consumer nativePointConsumer
	var raw []byte
	consumer.pointID = pointID
	err := tx.QueryRowContext(ctx, "SELECT kind,plan_id,apply_operation_id,requester,binding_id,epoch_id,generation,generation_id,policy_sha256,canonical FROM data_native_point_consumers WHERE point_id=?", pointID).Scan(&consumer.kind, &consumer.planID, &consumer.applyOperationID, &consumer.requester, &consumer.binding, &consumer.epoch, &consumer.generation, &consumer.generationID, &consumer.policySHA256, &raw)
	if err != nil {
		return nil, err
	}
	canonical, err := consumer.canonical()
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, &IntegrityError{}
	}
	return raw, nil
}

func checkNativePointConsumer(ctx context.Context, tx *sql.Tx, point nativePreparedPoint, c nativePointConsumer) error {
	v := point.view
	now := time.Now().UTC()
	if c.binding != v.BindingID || c.epoch != v.EpochID || c.generation != v.Generation || c.generationID != v.GenerationID || !data.ValidCatalogHash(c.policySHA256) || !digestPattern.MatchString(c.planID) || now.Before(v.PreparedAt) || !now.Before(v.ExpiresAt) || !now.Before(v.RetainUntil) {
		return ErrConflict
	}
	var requester, reference, state, kind string
	err := tx.QueryRowContext(ctx, "SELECT requester,secret_ref,state,kind FROM operations WHERE id=?", c.applyOperationID).Scan(&requester, &reference, &state, &kind)
	expected := map[nativePointConsumerKind]string{nativePointModulePublish: "module_publish_apply", nativePointLiveRestore: "native_live_restore", nativePointRetire: "native_retire"}
	if err != nil || expected[c.kind] == "" || kind != expected[c.kind] || requester != c.requester || reference != c.planID || state != "preflight" {
		return errors.Join(ErrConflict, err)
	}
	if err := checkNativePointCurrent(ctx, tx, v); err != nil {
		return err
	}
	var policyHash string
	if err := tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", v.DatabaseID).Scan(&policyHash); err != nil || strings.TrimPrefix(policyHash, "sha256:") != c.policySHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
