package datapublish

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type ReviewView struct {
	PlanID          string                          `json:"plan_id"`
	App             string                          `json:"app"`
	PointID         string                          `json:"point_id"`
	SourceKind      SourceKind                      `json:"source_kind"`
	WasmSHA256      string                          `json:"wasm_sha256"`
	ProgramWireHash string                          `json:"program_wire_hash"`
	SchemaSHA256    string                          `json:"schema_sha256"`
	Requirements    []spacetimedb.SchemaRequirement `json:"requirements"`
	Recovery        RecoveryReview                  `json:"recovery"`
	ExpiresAt       time.Time                       `json:"expires_at"`
}

func (p Plan) ReviewView() ReviewView {
	return ReviewView{PlanID: p.ID(), App: p.Facts().App, PointID: p.Source().PointID(), SourceKind: p.Source().Kind(), WasmSHA256: p.Candidate().WasmSHA256(), ProgramWireHash: p.Candidate().ProgramWireHash(), SchemaSHA256: p.Candidate().Schema().SHA256(), Requirements: p.Requirements(), Recovery: p.Review().Recovery, ExpiresAt: p.ExpiresAt()}
}

func (v ReviewView) Valid() bool {
	if !planID(v.PlanID) || !appName.MatchString(v.App) || !identity(v.PointID) || !digest(v.WasmSHA256) || !digest(v.ProgramWireHash) || !digest(v.SchemaSHA256) || !utc(v.ExpiresAt) || !v.Recovery.valid(v.SourceKind, v.SchemaSHA256) || len(v.Requirements) == 0 || len(v.Requirements) > 16 {
		return false
	}
	seen := make(map[spacetimedb.SchemaRequirement]bool)
	for _, requirement := range v.Requirements {
		if seen[requirement] {
			return false
		}
		seen[requirement] = true
		switch requirement {
		case spacetimedb.ReviewGuestCode, spacetimedb.VerifyNewTableData, spacetimedb.VerifyPreservedData, spacetimedb.VerifyDefaultProjection, spacetimedb.QualifyCallableEffects, spacetimedb.QualifyLifecycleEffects, spacetimedb.QualifySchedulerEffects:
		default:
			return false
		}
	}
	return true
}

func DecodeReviewView(raw []byte) (ReviewView, error) {
	var view ReviewView
	if len(raw) > 16384 {
		return view, ErrRecord
	}
	fields := []string{"plan_id", "app", "point_id", "source_kind", "wasm_sha256", "program_wire_hash", "schema_sha256", "requirements", "recovery", "expires_at"}
	if _, err := objectValues(raw, fields...); err != nil || json.Unmarshal(raw, &view) != nil || !view.Valid() {
		return ReviewView{}, ErrRecord
	}
	return view, nil
}
