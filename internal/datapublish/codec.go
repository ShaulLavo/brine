package datapublish

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type schemaReference struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
type candidateRecord struct {
	WasmSHA256      string          `json:"wasm_sha256"`
	ProgramWireHash string          `json:"program_wire_hash"`
	Schema          schemaReference `json:"schema"`
}
type capturedRecord struct {
	Point    CapturedPoint    `json:"point"`
	Schema   schemaReference  `json:"schema"`
	Contract CoverageContract `json:"coverage_contract"`
}
type sourceRecord struct {
	Kind     SourceKind      `json:"kind"`
	Empty    *EmptySource    `json:"empty,omitempty"`
	Captured *capturedRecord `json:"captured,omitempty"`
}
type planRecord struct {
	Version            int                `json:"version"`
	ID                 string             `json:"id,omitempty"`
	Requester          datainit.Requester `json:"requester"`
	Facts              BindingFacts       `json:"facts"`
	Source             sourceRecord       `json:"source"`
	Candidate          candidateRecord    `json:"candidate"`
	Review             ReviewReferences   `json:"review"`
	RequirementsSHA256 string             `json:"requirements_sha256"`
	Preflight          *NativePreflight   `json:"preflight,omitempty"`
	CreationName       string             `json:"creation_name,omitempty"`
	IssuedAt           time.Time          `json:"issued_at"`
	ExpiresAt          time.Time          `json:"expires_at"`
}

func schemaRef(schema spacetimedb.CanonicalSchema) schemaReference {
	return schemaReference{Version: schema.Version(), SHA256: schema.SHA256()}
}
func (p Plan) record(includeID bool) planRecord {
	record := planRecord{Version: RecordVersion, Requester: p.requester, Facts: p.facts,
		Source: sourceRecord{Kind: p.source.kind}, Candidate: candidateRecord{p.candidate.wasm, p.candidate.program, schemaRef(p.candidate.schema)},
		Review: p.review, RequirementsSHA256: p.requirementsSHA, IssuedAt: p.issuedAt, ExpiresAt: p.expiresAt}
	if includeID {
		record.ID = p.id
	}
	if p.source.kind == CreateSource {
		record.Source.Empty = &p.source.empty
		record.CreationName = p.creationName
		return record
	}
	record.Source.Captured = &capturedRecord{p.source.point, schemaRef(p.source.schema), ConfirmedRecoveryPairsV1}
	record.Preflight = &p.preflight
	return record
}

// MarshalJSON is the bounded private storage codec, not a public plan view.
func (p Plan) MarshalJSON() ([]byte, error) {
	if !planID(p.id) {
		return nil, ErrRecord
	}
	raw, err := json.Marshal(p.record(true))
	if err != nil || len(raw) > RecordLimit {
		return nil, ErrRecord
	}
	return raw, nil
}

// DecodePlan needs bound opaque schemas so persisted labels cannot replace the
// actual derived diff requirements. It does not authenticate storage or effects.
func DecodePlan(raw []byte, sourceSchema, candidateSchema spacetimedb.CanonicalSchema) (Plan, error) {
	if len(raw) == 0 || len(raw) > RecordLimit {
		return Plan{}, ErrRecord
	}
	fields, err := planFields(raw)
	if err != nil {
		return Plan{}, err
	}
	var record planRecord
	if json.Unmarshal(raw, &record) != nil || record.Version != RecordVersion || !planID(record.ID) || record.Candidate.Schema != schemaRef(candidateSchema) {
		return Plan{}, ErrRecord
	}
	if err = validatePlanObjects(fields, record.Source.Kind); err != nil {
		return Plan{}, err
	}
	source, err := decodeSource(record.Source, sourceSchema)
	if err != nil {
		return Plan{}, err
	}
	candidate, err := NewCandidate(record.Candidate.WasmSHA256, record.Candidate.ProgramWireHash, candidateSchema)
	if err != nil {
		return Plan{}, err
	}
	preflight := NativePreflight{}
	if record.Preflight != nil {
		preflight = *record.Preflight
	}
	p, err := NewPlan(PlanSpec{Requester: record.Requester, Facts: record.Facts, Source: source, Candidate: candidate, Review: record.Review,
		Preflight: preflight, CreationName: record.CreationName, IssuedAt: record.IssuedAt, ExpiresAt: record.ExpiresAt})
	if err != nil || p.ID() != record.ID || p.requirementsSHA != record.RequirementsSHA256 {
		return Plan{}, ErrRecord
	}
	return p, nil
}

func planFields(raw []byte) (map[string]json.RawMessage, error) {
	var kind struct {
		Source struct {
			Kind SourceKind `json:"kind"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &kind) != nil {
		return nil, ErrRecord
	}
	fields := []string{"version", "id", "requester", "facts", "source", "candidate", "review", "requirements_sha256", "issued_at", "expires_at"}
	switch kind.Source.Kind {
	case CreateSource:
		fields = append(fields, "creation_name")
	case CapturedSource:
		fields = append(fields, "preflight")
	default:
		return nil, ErrRecord
	}
	value, err := strictjson.Object(raw, fields...)
	if err != nil {
		return nil, ErrRecord
	}
	return value, nil
}

func decodeSource(record sourceRecord, schema spacetimedb.CanonicalSchema) (Source, error) {
	switch record.Kind {
	case CreateSource:
		if record.Empty == nil || record.Captured != nil || schema.SHA256() != "" {
			return Source{}, ErrRecord
		}
		return NewEmptySource(*record.Empty)
	case CapturedSource:
		if record.Empty != nil || record.Captured == nil || record.Captured.Contract != ConfirmedRecoveryPairsV1 || record.Captured.Schema != schemaRef(schema) {
			return Source{}, ErrRecord
		}
		return NewCapturedSource(record.Captured.Point, schema)
	default:
		return Source{}, ErrRecord
	}
}
