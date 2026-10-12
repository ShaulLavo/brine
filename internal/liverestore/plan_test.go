package liverestore

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
)

func planFixture(t *testing.T) PlanSpec {
	t.Helper()
	id, hash := strings.Repeat("a", 32), strings.Repeat("b", 64)
	oldID := strings.Repeat("c", 32)
	requester, err := datainit.ParseRequester("deploy:" + hash)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)
	facts := Facts{App: "fixture", IncarnationID: data.AppIncarnationID(id), DatabaseID: data.DatabaseID(id),
		Head:       data.EngineHeadRef{BindingID: data.ReplicaBindingID(id), EpochID: data.ReplicaEpochID(id), Version: 3, RuntimeSHA256: hash},
		Generation: 3, GenerationID: id, PolicySHA256: hash, TargetSHA256: hash, DesiredSHA256: hash}
	safety := Safety{PointID: id, PointSHA256: hash, FenceID: data.FenceID(id), FenceOwnerOpID: "prepared-operation", SourceSealSHA256: hash,
		SourceWitnessSHA256: hash, StopProofSHA256: hash, CheckpointSHA256: hash, CommittedOffset: 21,
		PreparedAt: at.Add(-time.Minute), ExpiresAt: at.Add(10 * time.Minute)}
	selected := SelectedPoint{IncarnationID: facts.IncarnationID, DatabaseID: facts.DatabaseID, BindingID: facts.Head.BindingID,
		PointID: oldID, PointSHA256: hash, EpochID: facts.Head.EpochID, GenerationID: id, CaptureID: oldID,
		RepositoryID: hash, RepositorySnapshot: hash, ManifestSHA256: hash, RestoreReceiptSHA256: hash, CheckpointSHA256: hash,
		StateWitnessSHA256: hash, NativeDatabase: hash, NativePublisher: hash, ProgramWireHash: hash, ArtifactWasmSHA256: hash,
		SchemaSHA256: hash, MaterialSHA256: hash, DependenciesSHA256: hash, CommittedOffset: 8,
		RecordedAt: at.Add(-time.Hour), RetainUntil: at.Add(30 * 24 * time.Hour)}
	return PlanSpec{Requester: requester, Facts: facts, Safety: safety, Selected: selected,
		Recovery: RecoveryRelease{ReleaseID: "recovery-release-operation", CodeSHA256: hash, CompatibilitySHA256: hash, ProgramWireHash: hash, SchemaSHA256: hash},
		IssuedAt: at, ExpiresAt: at.Add(5 * time.Minute)}
}

func TestPlanKeepsCurrentSafetySeparateFromSelectedHistoricalLoss(t *testing.T) {
	spec := planFixture(t)
	p, err := NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec().Safety.PointID != spec.Safety.PointID || p.Spec().Selected.PointID != spec.Selected.PointID ||
		p.Loss() != (Loss{Kind: "known", SelectedOffset: 8, CurrentOffset: 21, CommittedOffsetsLost: 13}) {
		t.Fatal("current protection or historical selection changed")
	}
	decoded, err := DecodePlan(p.Bytes())
	if err != nil || decoded.ID() != p.ID() || decoded.Spec() != spec {
		t.Fatal("immutable plan did not round trip", err)
	}
	raw := p.Bytes()
	raw[0] = 'x'
	if bytes.Equal(p.Bytes(), raw) {
		t.Fatal("caller changed retained plan bytes")
	}
}

func TestDifferentNativeHistoryMakesLossUnknown(t *testing.T) {
	for _, field := range []string{"epoch", "generation"} {
		t.Run(field, func(t *testing.T) {
			spec := planFixture(t)
			if field == "epoch" {
				spec.Selected.EpochID = data.ReplicaEpochID(strings.Repeat("d", 32))
			}
			if field == "generation" {
				spec.Selected.GenerationID = strings.Repeat("d", 32)
			}
			spec.Selected.CommittedOffset = 100
			p, err := NewPlan(spec)
			if err != nil || p.Loss().Kind != "unknown" || p.Loss().UnknownReason == "" || p.Loss().CommittedOffsetsLost != 0 {
				t.Fatal("unrelated offset histories were compared", err)
			}
		})
	}
}

func TestPlanRejectsWrongScopeAndIncompatibleRecovery(t *testing.T) {
	cases := map[string]func(*PlanSpec){
		"selected-as-safety":  func(s *PlanSpec) { s.Selected.PointID = s.Safety.PointID },
		"foreign-binding":     func(s *PlanSpec) { s.Selected.BindingID = data.ReplicaBindingID(strings.Repeat("e", 32)) },
		"foreign-database":    func(s *PlanSpec) { s.Selected.DatabaseID = data.DatabaseID(strings.Repeat("e", 32)) },
		"foreign-incarnation": func(s *PlanSpec) { s.Selected.IncarnationID = data.AppIncarnationID(strings.Repeat("e", 32)) },
		"future-offset":       func(s *PlanSpec) { s.Selected.CommittedOffset = 22 },
		"wrong-module":        func(s *PlanSpec) { s.Recovery.ProgramWireHash = strings.Repeat("e", 64) },
		"wrong-schema":        func(s *PlanSpec) { s.Recovery.SchemaSHA256 = strings.Repeat("e", 64) },
		"expired-safety":      func(s *PlanSpec) { s.Safety.ExpiresAt = s.IssuedAt },
		"short-retention":     func(s *PlanSpec) { s.Selected.RetainUntil = s.IssuedAt },
		"future-source":       func(s *PlanSpec) { s.Selected.RecordedAt = s.IssuedAt },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := planFixture(t)
			mutate(&spec)
			if _, err := NewPlan(spec); err == nil {
				t.Fatal("invalid review admitted")
			}
		})
	}
}

func TestPlanFreshnessBindsSelectedRepositoryAndOriginalFence(t *testing.T) {
	spec := planFixture(t)
	p, err := NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	selected := spec.Selected
	selected.RepositorySnapshot = strings.Repeat("e", 64)
	if p.CheckFreshness(spec.Requester, spec.Facts, spec.Safety, selected, spec.IssuedAt) == nil {
		t.Fatal("latest snapshot replaced selected immutable snapshot")
	}
	safety := spec.Safety
	safety.FenceOwnerOpID = "other-operation"
	if p.CheckFreshness(spec.Requester, spec.Facts, safety, spec.Selected, spec.IssuedAt) == nil {
		t.Fatal("original fence owner changed")
	}
	if p.CheckFreshness(spec.Requester, spec.Facts, spec.Safety, spec.Selected, spec.ExpiresAt) == nil {
		t.Fatal("expired review admitted")
	}
	if err = p.CheckFreshness(spec.Requester, spec.Facts, spec.Safety, spec.Selected, spec.IssuedAt); err != nil {
		t.Fatal(err)
	}
}

func TestDecoderRejectsTamperedLossAndNestedAuthority(t *testing.T) {
	p, err := NewPlan(planFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		bytes.Replace(p.Bytes(), []byte(`"committed_offsets_lost":13`), []byte(`"committed_offsets_lost":0`), 1),
		bytes.Replace(p.Bytes(), []byte(`"fence_owner_op_id":`), []byte(`"path":"/tmp/owned","fence_owner_op_id":`), 1),
		bytes.Replace(p.Bytes(), []byte(`"point_id":`), []byte(`"force":true,"point_id":`), 1),
		bytes.Replace(p.Bytes(), []byte(`"generation":3`), []byte(`"generation":3,"generation":3`), 1),
	} {
		if _, err = DecodePlan(raw); err == nil {
			t.Fatal("tampered record admitted")
		}
	}
}

func TestRequestsAcceptReferencesAndRefuseAuthorityFields(t *testing.T) {
	spec := planFixture(t)
	request := PlanRequest{App: "fixture", Database: "db", SelectedPointID: spec.Selected.PointID,
		CurrentPreparedPointID: spec.Safety.PointID, RecoveryReleaseID: spec.Recovery.ReleaseID}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if observed, err := DecodePlanRequest(raw); err != nil || observed != request {
		t.Fatal("reference request refused", err)
	}
	for _, field := range []string{"path", "token", "key", "force", "requester", "proof", "command"} {
		injected := append([]byte(`{"`+field+`":"supplied",`), raw[1:]...)
		if _, err = DecodePlanRequest(injected); err == nil {
			t.Fatalf("caller authority field %s admitted", field)
		}
	}
}
