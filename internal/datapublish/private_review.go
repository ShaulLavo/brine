package datapublish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

const PrivateReviewLimit = 3 << 20
const NativePreflightResponseLimit = 64 << 10
const NativePreflightTokenLimit = 8 << 10

// PrivateReview retains exact reviewed material, including a private token.
// Construction validates bindings only; host provenance and role lookup remain
// prerequisites before store claim or effects.
type PrivateReview struct {
	plan       Plan
	artifact   Artifact
	source     []byte
	descriptor []byte
	preflight  []byte
	token      []byte
}

func NewPrivateReview(plan Plan, artifact Artifact, sourceSchemaV10, descriptor, preflightResponse, token []byte) (PrivateReview, error) {
	if !planID(plan.id) || string(artifact.wasm) != plan.candidate.wasm || string(artifact.program) != plan.candidate.program || len(artifact.bytes) == 0 ||
		len(sourceSchemaV10) > spacetimedb.DefaultSchemaLimits().MaxBytes || len(descriptor) > spacetimedb.DefaultSchemaLimits().MaxBytes ||
		len(preflightResponse) > NativePreflightResponseLimit || len(token) > NativePreflightTokenLimit {
		return PrivateReview{}, ErrRecord
	}
	candidate, err := spacetimedb.DecodeDescriptorV10(descriptor, spacetimedb.DefaultSchemaLimits())
	if err != nil || candidate.SHA256() != plan.candidate.schema.SHA256() {
		return PrivateReview{}, ErrRecord
	}
	if err = checkPrivateSource(plan, sourceSchemaV10, preflightResponse, token); err != nil {
		return PrivateReview{}, err
	}
	review := PrivateReview{plan: plan, artifact: artifact, source: slices.Clone(sourceSchemaV10), descriptor: slices.Clone(descriptor), preflight: slices.Clone(preflightResponse), token: slices.Clone(token)}
	raw, err := review.StorageBytes()
	if err != nil || len(raw) > PrivateReviewLimit {
		return PrivateReview{}, ErrRecord
	}
	return review, nil
}

func checkPrivateSource(plan Plan, raw, response, token []byte) error {
	if plan.source.kind == CreateSource {
		if len(raw) != 0 || len(response) != 0 || len(token) != 0 {
			return ErrRecord
		}
		return nil
	}
	source, err := spacetimedb.CanonicalSchemaV10(raw, spacetimedb.DefaultSchemaLimits())
	if err != nil || source.SHA256() != plan.source.schema.SHA256() || len(response) == 0 || len(token) == 0 || privateBytesSHA256(response) != plan.preflight.ResponseSHA256 || privateBytesSHA256(token) != plan.preflight.TokenSHA256 {
		return ErrRecord
	}
	return nil
}

func privateBytesSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r PrivateReview) Plan() Plan                      { return r.plan }
func (r PrivateReview) Artifact() Artifact              { return r.artifact }
func (r PrivateReview) SourceSchemaV10() []byte         { return slices.Clone(r.source) }
func (r PrivateReview) CandidateDescriptor() []byte     { return slices.Clone(r.descriptor) }
func (r PrivateReview) NativePreflightResponse() []byte { return slices.Clone(r.preflight) }
func (r PrivateReview) NativePreflightToken() []byte    { return slices.Clone(r.token) }
func (PrivateReview) String() string                    { return "[private publication review]" }
func (PrivateReview) GoString() string                  { return "[private publication review]" }
func (PrivateReview) MarshalJSON() ([]byte, error)      { return nil, ErrRecord }

type privateReviewRecord struct {
	Version    int             `json:"version"`
	Plan       json.RawMessage `json:"plan"`
	Artifact   []byte          `json:"artifact"`
	Source     []byte          `json:"source_schema_v10,omitempty"`
	Descriptor []byte          `json:"candidate_descriptor"`
	Preflight  []byte          `json:"native_preflight_response,omitempty"`
	Token      []byte          `json:"native_preflight_token,omitempty"`
}

// StorageBytes is private control-state content, never a public plan response.
func (r PrivateReview) StorageBytes() ([]byte, error) {
	raw, err := r.plan.MarshalJSON()
	if err != nil {
		return nil, ErrRecord
	}
	encoded, err := json.Marshal(privateReviewRecord{RecordVersion, raw, r.artifact.bytes, r.source, r.descriptor, r.preflight, r.token})
	if err != nil || len(encoded) > PrivateReviewLimit {
		return nil, ErrRecord
	}
	return encoded, nil
}

// DecodePrivateReview validates private stored syntax. Its source, extraction
// and token bytes require independent host authentication before effects.
func DecodePrivateReview(raw []byte) (PrivateReview, error) {
	if len(raw) == 0 || len(raw) > PrivateReviewLimit {
		return PrivateReview{}, ErrRecord
	}
	var value privateReviewRecord
	if json.Unmarshal(raw, &value) != nil || value.Version != RecordVersion {
		return PrivateReview{}, ErrRecord
	}
	var kind struct {
		Source struct {
			Kind SourceKind `json:"kind"`
		} `json:"source"`
	}
	if json.Unmarshal(value.Plan, &kind) != nil {
		return PrivateReview{}, ErrRecord
	}
	fields := []string{"version", "plan", "artifact", "candidate_descriptor"}
	if kind.Source.Kind == CapturedSource {
		fields = append(fields, "source_schema_v10", "native_preflight_response", "native_preflight_token")
	}
	if _, err := objectValues(raw, fields...); err != nil {
		return PrivateReview{}, ErrRecord
	}
	artifact, err := NewArtifact(value.Artifact)
	if err != nil {
		return PrivateReview{}, err
	}
	candidate, err := spacetimedb.DecodeDescriptorV10(value.Descriptor, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		return PrivateReview{}, ErrRecord
	}
	var source spacetimedb.CanonicalSchema
	if kind.Source.Kind == CapturedSource {
		source, err = spacetimedb.CanonicalSchemaV10(value.Source, spacetimedb.DefaultSchemaLimits())
		if err != nil {
			return PrivateReview{}, ErrRecord
		}
	}
	plan, err := DecodePlan(value.Plan, source, candidate)
	if err != nil {
		return PrivateReview{}, err
	}
	return NewPrivateReview(plan, artifact, value.Source, value.Descriptor, value.Preflight, value.Token)
}
