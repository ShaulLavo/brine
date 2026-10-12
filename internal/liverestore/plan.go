// Package liverestore records native recovery review. Records describe expected
// evidence; protected store joins establish authority before any host effect.
package liverestore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
)

const Format = "brine-native-live-restore-plan-v1"
const RecordLimit = 64 << 10
const MaxReviewLifetime = 15 * time.Minute

var ErrRecord = errors.New("invalid live restore record")
var ErrStale = errors.New("live restore review is stale")
var operationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var appName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Facts struct {
	App           string                `json:"app"`
	IncarnationID data.AppIncarnationID `json:"incarnation_id"`
	DatabaseID    data.DatabaseID       `json:"database_id"`
	Head          data.EngineHeadRef    `json:"head"`
	Generation    uint64                `json:"generation"`
	GenerationID  string                `json:"generation_id"`
	PolicySHA256  string                `json:"policy_sha256"`
	TargetSHA256  string                `json:"target_sha256"`
	DesiredSHA256 string                `json:"desired_sha256"`
}

func (f Facts) valid() bool {
	return appName.MatchString(f.App) && data.ValidID(string(f.IncarnationID)) && data.ValidID(string(f.DatabaseID)) &&
		f.Head.Validate() == nil && f.Generation <= 1<<63-1 && data.ValidID(f.GenerationID) &&
		digest(f.PolicySHA256) && digest(f.TargetSHA256) && digest(f.DesiredSHA256)
}

// Safety names the fresh exact prepared point protecting the current head.
// A historical selected point cannot supply this current preparation evidence.
type Safety struct {
	PointID              string       `json:"point_id"`
	PointSHA256          string       `json:"point_sha256"`
	FenceID              data.FenceID `json:"fence_id"`
	FenceOwnerOpID       string       `json:"fence_owner_op_id"`
	SourceSealSHA256     string       `json:"source_seal_sha256"`
	SourceWitnessSHA256  string       `json:"source_witness_sha256"`
	StopProofSHA256      string       `json:"stop_proof_sha256"`
	CheckpointSHA256     string       `json:"checkpoint_sha256"`
	CommittedOffset      uint64       `json:"committed_offset"`
	CommittedOffsetKnown bool         `json:"committed_offset_known"`
	PreparedAt           time.Time    `json:"prepared_at"`
	ExpiresAt            time.Time    `json:"expires_at"`
}

func (s Safety) valid() bool {
	return data.ValidID(s.PointID) && data.ValidID(string(s.FenceID)) && operationID.MatchString(s.FenceOwnerOpID) &&
		digest(s.PointSHA256) && digest(s.SourceSealSHA256) && digest(s.SourceWitnessSHA256) &&
		digest(s.StopProofSHA256) && digest(s.CheckpointSHA256) && (s.CommittedOffsetKnown || s.CommittedOffset == 0) && lifetime(s.PreparedAt, s.ExpiresAt)
}

// SelectedPoint is an immutable review description obtained from protected
// recovery records. Syntax and hash equality alone do not authenticate it.
type SelectedPoint struct {
	IncarnationID         data.AppIncarnationID `json:"incarnation_id"`
	DatabaseID            data.DatabaseID       `json:"database_id"`
	BindingID             data.ReplicaBindingID `json:"binding_id"`
	PointID               string                `json:"point_id"`
	PointSHA256           string                `json:"point_sha256"`
	EpochID               data.ReplicaEpochID   `json:"epoch_id"`
	GenerationID          string                `json:"generation_id"`
	CaptureID             string                `json:"capture_id"`
	RepositoryID          string                `json:"repository_id"`
	RepositorySnapshot    string                `json:"repository_snapshot"`
	ManifestSHA256        string                `json:"manifest_sha256"`
	RestoreEvidenceSHA256 string                `json:"restore_evidence_sha256"`
	CheckpointSHA256      string                `json:"checkpoint_sha256"`
	StateWitnessSHA256    string                `json:"state_witness_sha256"`
	NativeDatabase        string                `json:"native_database"`
	NativePublisher       string                `json:"native_publisher"`
	ProgramWireHash       string                `json:"program_wire_hash"`
	ArtifactWasmSHA256    string                `json:"artifact_wasm_sha256"`
	SchemaSHA256          string                `json:"schema_sha256"`
	MaterialSHA256        string                `json:"material_sha256"`
	DependenciesSHA256    string                `json:"dependencies_sha256"`
	CommittedOffset       uint64                `json:"committed_offset"`
	RecordedAt            time.Time             `json:"recorded_at"`
	RetainUntil           time.Time             `json:"retain_until"`
}

func (s SelectedPoint) valid() bool {
	if !data.ValidID(string(s.IncarnationID)) || !data.ValidID(string(s.DatabaseID)) || !data.ValidID(string(s.BindingID)) ||
		!data.ValidID(s.PointID) || !data.ValidID(string(s.EpochID)) || !data.ValidID(s.GenerationID) || !data.ValidID(s.CaptureID) ||
		!utc(s.RecordedAt) || !utc(s.RetainUntil) || !s.RetainUntil.After(s.RecordedAt) {
		return false
	}
	for _, value := range []string{s.PointSHA256, s.RepositoryID, s.RepositorySnapshot, s.ManifestSHA256, s.RestoreEvidenceSHA256,
		s.CheckpointSHA256, s.StateWitnessSHA256, s.NativeDatabase, s.NativePublisher, s.ProgramWireHash, s.ArtifactWasmSHA256,
		s.SchemaSHA256, s.MaterialSHA256, s.DependenciesSHA256} {
		if !digest(value) {
			return false
		}
	}
	return true
}

type RecoveryRelease struct {
	ReleaseID           string `json:"release_id"`
	CodeSHA256          string `json:"code_sha256"`
	CompatibilitySHA256 string `json:"compatibility_sha256"`
	ProgramWireHash     string `json:"program_wire_hash"`
	SchemaSHA256        string `json:"schema_sha256"`
}

func (r RecoveryRelease) validFor(s SelectedPoint) bool {
	return operationID.MatchString(r.ReleaseID) && digest(r.CodeSHA256) && digest(r.CompatibilitySHA256) &&
		r.ProgramWireHash == s.ProgramWireHash && r.SchemaSHA256 == s.SchemaSHA256
}

type PlanSpec struct {
	Requester datainit.Requester `json:"requester"`
	Facts     Facts              `json:"facts"`
	Safety    Safety             `json:"safety"`
	Selected  SelectedPoint      `json:"selected"`
	Recovery  RecoveryRelease    `json:"recovery"`
	IssuedAt  time.Time          `json:"issued_at"`
	ExpiresAt time.Time          `json:"expires_at"`
}

type Plan struct {
	spec    PlanSpec
	loss    Loss
	encoded []byte
	id      string
}

func NewPlan(spec PlanSpec) (Plan, error) {
	if !spec.Requester.Valid() || !spec.Facts.valid() || !spec.Safety.valid() || !spec.Selected.valid() ||
		!spec.Recovery.validFor(spec.Selected) || !lifetime(spec.IssuedAt, spec.ExpiresAt) ||
		spec.Safety.PreparedAt.After(spec.IssuedAt) || spec.Safety.ExpiresAt.Before(spec.ExpiresAt) ||
		spec.Selected.RecordedAt.After(spec.Safety.PreparedAt) || spec.Selected.RetainUntil.Before(spec.ExpiresAt) || spec.Selected.PointID == spec.Safety.PointID ||
		spec.Selected.IncarnationID != spec.Facts.IncarnationID || spec.Selected.DatabaseID != spec.Facts.DatabaseID || spec.Selected.BindingID != spec.Facts.Head.BindingID {
		return Plan{}, ErrRecord
	}
	loss, err := deriveLoss(spec)
	if err != nil {
		return Plan{}, err
	}
	raw, err := json.Marshal(planRecord{Format: Format, Spec: spec, Loss: loss})
	if err != nil || len(raw) > RecordLimit {
		return Plan{}, ErrRecord
	}
	sum := sha256.Sum256(raw)
	return Plan{spec: spec, loss: loss, encoded: raw, id: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

func (p Plan) ID() string     { return p.id }
func (p Plan) Spec() PlanSpec { return p.spec }
func (p Plan) Loss() Loss     { return p.loss }
func (p Plan) Bytes() []byte  { return bytes.Clone(p.encoded) }

func (p Plan) CheckFreshness(requester datainit.Requester, current Facts, safety Safety, selected SelectedPoint, now time.Time) error {
	if !planID(p.id) || requester != p.spec.Requester || current != p.spec.Facts || safety != p.spec.Safety || selected != p.spec.Selected ||
		!utc(now) || now.Before(p.spec.IssuedAt) || !now.Before(p.spec.ExpiresAt) {
		return ErrStale
	}
	return nil
}

func digest(value string) bool { return data.ValidCatalogHash(value) }
func planID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && digest(strings.TrimPrefix(value, "sha256:"))
}
func utc(value time.Time) bool { return !value.IsZero() && value.Location() == time.UTC }
func lifetime(issued, expires time.Time) bool {
	return utc(issued) && utc(expires) && expires.After(issued) && expires.Sub(issued) <= MaxReviewLifetime
}
