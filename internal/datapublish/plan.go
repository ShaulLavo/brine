package datapublish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type PlanSpec struct {
	Requester    datainit.Requester
	Facts        BindingFacts
	Source       Source
	Candidate    Candidate
	Review       ReviewReferences
	Preflight    NativePreflight
	CreationName string
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

type Plan struct {
	id              string
	requester       datainit.Requester
	facts           BindingFacts
	source          Source
	candidate       Candidate
	review          ReviewReferences
	preflight       NativePreflight
	creationName    string
	issuedAt        time.Time
	expiresAt       time.Time
	diff            spacetimedb.SchemaDiff
	requirements    []spacetimedb.SchemaRequirement
	requirementsSHA string
}

func NewPlan(spec PlanSpec) (Plan, error) {
	if !spec.Requester.Valid() || !spec.Facts.valid() || !validLifetime(spec.IssuedAt, spec.ExpiresAt) || !validReview(spec.Review, spec.Source.Kind(), spec.Candidate) ||
		!digest(spec.Candidate.wasm) || !digest(spec.Candidate.program) || spec.Candidate.schema.Version() != spacetimedb.CanonicalizerVersion {
		return Plan{}, ErrRecord
	}
	p := Plan{requester: spec.Requester, facts: spec.Facts, source: spec.Source, candidate: spec.Candidate,
		review: spec.Review, preflight: spec.Preflight, creationName: spec.CreationName, issuedAt: spec.IssuedAt, expiresAt: spec.ExpiresAt}
	p.review.Recovery = spec.Review.Recovery.clone()
	if err := p.deriveRequirements(); err != nil {
		return Plan{}, err
	}
	p.requirementsSHA = p.requirementFingerprint()
	raw, err := json.Marshal(p.record(false))
	if err != nil || len(raw) > RecordLimit {
		return Plan{}, ErrRecord
	}
	sum := sha256.Sum256(raw)
	p.id = "sha256:" + hex.EncodeToString(sum[:])
	return p, nil
}

func (p Plan) requirementFingerprint() string {
	raw, _ := json.Marshal(struct {
		Kind         SourceKind                      `json:"source_kind"`
		Decision     spacetimedb.SchemaDecision      `json:"schema_decision"`
		Changes      []spacetimedb.SchemaChange      `json:"changes"`
		Requirements []spacetimedb.SchemaRequirement `json:"requirements"`
	}{p.source.kind, p.diff.Decision(), p.diff.Changes(), p.requirements})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validLifetime(issued, expires time.Time) bool {
	return utc(issued) && utc(expires) && expires.After(issued) && expires.Sub(issued) <= MaxReviewLifetime
}

func validReview(review ReviewReferences, source SourceKind, candidate Candidate) bool {
	return review.GuestCodeSHA256 == candidate.WasmSHA256() && digest(review.GuestCodeSHA256) && review.Recovery.valid(source, candidate.Schema().SHA256())
}

func (p *Plan) deriveRequirements() error {
	switch p.source.kind {
	case CreateSource:
		if !appName.MatchString(p.creationName) || p.preflight != (NativePreflight{}) || p.source.empty.ConfigSHA256 != p.facts.ConfigSHA256 {
			return ErrRecord
		}
		p.requirements = []spacetimedb.SchemaRequirement{spacetimedb.ReviewGuestCode, spacetimedb.VerifyNewTableData}
		return nil
	case CapturedSource:
		if p.creationName != "" || !p.preflight.valid() || p.source.point.ProgramWireHash == p.candidate.program || p.source.point.WasmSHA256 == p.candidate.wasm {
			return ErrRecord
		}
		diff, err := spacetimedb.CompareSchemas(p.source.schema, p.candidate.schema)
		if err != nil {
			return ErrRecord
		}
		if diff.Decision() != spacetimedb.StagedValidationRequired {
			return errors.Join(ErrRecord, ErrOperatorProcedure)
		}
		p.diff = diff
		p.requirements = diff.Requirements()
		return nil
	default:
		return ErrRecord
	}
}

func (p Plan) ID() string                    { return p.id }
func (p Plan) Requester() datainit.Requester { return p.requester }
func (p Plan) Facts() BindingFacts           { return p.facts }
func (p Plan) Source() Source                { return p.source }
func (p Plan) Candidate() Candidate          { return p.candidate }
func (p Plan) Review() ReviewReferences {
	value := p.review
	value.Recovery = value.Recovery.clone()
	return value
}
func (p Plan) Preflight() NativePreflight                    { return p.preflight }
func (p Plan) CreationName() string                          { return p.creationName }
func (p Plan) ExpiresAt() time.Time                          { return p.expiresAt }
func (p Plan) Requirements() []spacetimedb.SchemaRequirement { return slices.Clone(p.requirements) }
func (p Plan) Diff() spacetimedb.SchemaDiff                  { return p.diff }
func (p Plan) RequirementsSHA256() string                    { return p.requirementsSHA }

// CheckFreshness compares repeat inputs, not protected source or operator authority.
func (p Plan) CheckFreshness(requester datainit.Requester, facts BindingFacts, source Source, now time.Time) error {
	if !planID(p.id) || requester != p.requester || facts != p.facts || !utc(now) || now.Before(p.issuedAt) || !now.Before(p.expiresAt) || !sameSource(p.source, source) {
		return ErrStale
	}
	return nil
}

func sameSource(old, next Source) bool {
	return old.kind == next.kind && old.empty == next.empty && reflect.DeepEqual(old.point, next.point) && old.schema.SHA256() == next.schema.SHA256()
}
