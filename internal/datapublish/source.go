package datapublish

import (
	"slices"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

// EmptySource names a recovered owned server bundle, not a native database.
type EmptySource struct {
	AllocationID          string `json:"allocation_id"`
	PointID               string `json:"point_id"`
	FenceID               string `json:"fence_id"`
	PreparationOpID       string `json:"preparation_op_id"`
	RestoreReceiptSHA256  string `json:"restore_receipt_sha256"`
	BundleManifestSHA256  string `json:"bundle_manifest_sha256"`
	SourceSealSHA256      string `json:"source_seal_sha256"`
	ConfigSHA256          string `json:"config_sha256"`
	SigningRecoverySHA256 string `json:"signing_recovery_sha256"`
}

type CapturedPoint struct {
	PointID              string   `json:"point_id"`
	FenceID              string   `json:"fence_id"`
	PreparationOpID      string   `json:"preparation_op_id"`
	RestoreReceiptSHA256 string   `json:"restore_receipt_sha256"`
	BundleManifestSHA256 string   `json:"bundle_manifest_sha256"`
	SourceSealSHA256     string   `json:"source_seal_sha256"`
	StateWitnessSHA256   string   `json:"state_witness_sha256"`
	StopProofSHA256      string   `json:"stop_proof_sha256"`
	CheckpointSHA256     string   `json:"checkpoint_sha256"`
	NativeDatabase       string   `json:"native_database"`
	NativePublisher      string   `json:"native_publisher"`
	ProgramWireHash      string   `json:"program_wire_hash"`
	WasmSHA256           string   `json:"wasm_sha256"`
	Names                []string `json:"names"`
}

// Source has disjoint empty-server and initialized-history arms. Hash references
// bind later protected evidence; their syntax does not establish that evidence.
type Source struct {
	kind   SourceKind
	empty  EmptySource
	point  CapturedPoint
	schema spacetimedb.CanonicalSchema
}

func NewEmptySource(value EmptySource) (Source, error) {
	if !identity(value.AllocationID) || !identity(value.PointID) || !identity(value.FenceID) || !operationReference.MatchString(value.PreparationOpID) || !digest(value.RestoreReceiptSHA256) ||
		!digest(value.BundleManifestSHA256) || !digest(value.SourceSealSHA256) || !digest(value.ConfigSHA256) || !digest(value.SigningRecoverySHA256) {
		return Source{}, ErrRecord
	}
	return Source{kind: CreateSource, empty: value}, nil
}

func NewCapturedSource(point CapturedPoint, schema spacetimedb.CanonicalSchema) (Source, error) {
	if !identity(point.PointID) || !identity(point.FenceID) || !operationReference.MatchString(point.PreparationOpID) || !digest(point.RestoreReceiptSHA256) || !digest(point.BundleManifestSHA256) ||
		!digest(point.SourceSealSHA256) || !digest(point.StateWitnessSHA256) || !digest(point.StopProofSHA256) || !digest(point.CheckpointSHA256) ||
		!digest(point.NativeDatabase) || !digest(point.NativePublisher) || !digest(point.ProgramWireHash) || schema.Version() != spacetimedb.CanonicalizerVersion || !SupportsConfirmedRecoveryPairs(point.WasmSHA256, schema.SHA256()) {
		return Source{}, ErrRecord
	}
	if !validNames(point.Names) {
		return Source{}, ErrRecord
	}
	point.Names = slices.Clone(point.Names)
	return Source{kind: CapturedSource, point: point, schema: schema}, nil
}

func (s Source) Kind() SourceKind           { return s.kind }
func (s Source) Empty() (EmptySource, bool) { return s.empty, s.kind == CreateSource }
func (s Source) Captured() (CapturedPoint, bool) {
	point := s.point
	point.Names = slices.Clone(point.Names)
	return point, s.kind == CapturedSource
}
func (s Source) Schema() spacetimedb.CanonicalSchema { return s.schema }
func (s Source) FenceID() string {
	if s.kind == CreateSource {
		return s.empty.FenceID
	}
	return s.point.FenceID
}
func (s Source) PointID() string {
	if s.kind == CreateSource {
		return s.empty.PointID
	}
	return s.point.PointID
}
func (s Source) PreparationOpID() string {
	if s.kind == CreateSource {
		return s.empty.PreparationOpID
	}
	return s.point.PreparationOpID
}
func (s Source) SealSHA256() string {
	if s.kind == CreateSource {
		return s.empty.SourceSealSHA256
	}
	return s.point.SourceSealSHA256
}

func validNames(names []string) bool {
	if len(names) == 0 || len(names) > 16 || !slices.IsSorted(names) {
		return false
	}
	for i, name := range names {
		if !appName.MatchString(name) || i > 0 && names[i-1] == name {
			return false
		}
	}
	return true
}

// SupportsConfirmedRecoveryPairs identifies the finite fixture profiles whose
// fixed confirmed-state contract is supported. It grants no source authority.
func SupportsConfirmedRecoveryPairs(artifactWasmSHA256, canonicalSchemaSHA256 string) bool {
	var expected string
	switch artifactWasmSHA256 {
	case "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc":
		expected = "f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45"
	case "5f1de728e240ff189fdb47fae0af5f18cfa3c772b86a198220115a6869215ffb":
		expected = "23a8b7ac1656a127dbb519d45a0a35d88a114806e8933b554bede465b589cd7f"
	case "40960700aea4b6dd4d201539e91ff6fc27fcd96c050c92e182bf4c0240a03269":
		expected = "95ac506b0911df63146d79cdba646d1e059e122240c1f0349a34335e9749d07b"
	default:
		return false
	}
	return canonicalSchemaSHA256 == expected
}
