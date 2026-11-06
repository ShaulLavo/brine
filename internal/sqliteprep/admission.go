package sqliteprep

import (
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type Code string

const (
	LocalUncertain       Code = "local_uncertainty"
	StoppedSourceChanged Code = "stopped_source_changed"
	CoverageUnavailable  Code = "supersede_coverage_unavailable"
	CredentialsExpired   Code = "required_credentials_expired"
	InvalidEvidence      Code = "invalid_evidence"
	EffectUnknown        Code = "effect_unknown"
)

type Refusal struct{ Code Code }

func (r *Refusal) Error() string { return "SQLite preparation refused: " + string(r.Code) }
func refuse(code Code) error     { return &Refusal{Code: code} }

type ReplicaState string

const (
	Running ReplicaState = "running"
	Stopped ReplicaState = "stopped"
	Missing ReplicaState = "absent"
)

type Observation struct {
	Replica        ReplicaState
	WritersStopped bool
	JobsAbsent     bool
	Owned          bool
	Schema         data.SQLiteSchemaObservation
}
type Selection struct {
	Mode                Mode
	Arm                 Arm
	StopWritersRequired bool
	Origins
}

// Select performs no effects. A stopped observation must be made with held
// lifetime/source guards. Credentials are checked by the service before claim.
func Select(scope Scope, l *Lineage, o Observation, b *StoppedBundle, now time.Time) (Selection, error) {
	if scope.Validate() != nil || !utc(now) || !o.JobsAbsent || !o.Owned || !validSchema(o.Schema, scope.DatabaseID) || !sameSchema(scope.Schema, o.Schema) || o.Schema.ObservedAt.After(now) || o.Replica != Running && o.Replica != Stopped && o.Replica != Missing {
		return Selection{}, refuse(LocalUncertain)
	}
	if o.Replica != Running && (b == nil || b.Validate() != nil) {
		return Selection{}, refuse(LocalUncertain)
	}
	if l == nil {
		return freshSelection(scope, o, b)
	}
	origins, err := ResolveOrigins(*l)
	if err != nil || !sameLineageScope(l.Previous.Scope, scope) {
		return Selection{}, refuse(InvalidEvidence)
	}
	s := Selection{StopWritersRequired: !o.WritersStopped, Origins: origins}
	if o.Replica == Running {
		return selectLive(scope, s)
	}
	if origins.Baseline != nil && (!o.WritersStopped || !origins.Baseline.Matches(*b)) {
		return Selection{}, refuse(StoppedSourceChanged)
	}
	if origins.Source != nil && origins.Baseline != nil && o.WritersStopped {
		s.Mode = SupersedeReverify
		s.Arm = sourceArm(scope, *origins.Source)
		return s, nil
	}
	if !b.CaptureFits() {
		return Selection{}, refuse(CoverageUnavailable)
	}
	s.Mode = SupersedeCapture
	s.Arm = Snapshot
	s.CoverageOrigin = ""
	s.Source = nil
	if scope.EmptyAllocation != nil {
		s.Arm = Empty
	}
	return s, nil
}
func freshSelection(s Scope, o Observation, b *StoppedBundle) (Selection, error) {
	result := Selection{Mode: Fresh, Arm: Snapshot, StopWritersRequired: true}
	if o.Replica == Running {
		if !s.ReplicaCommitted {
			return Selection{}, refuse(LocalUncertain)
		}
		result.Arm = LTX
		return result, nil
	}
	if !b.CaptureFits() {
		return Selection{}, refuse(CoverageUnavailable)
	}
	if s.EmptyAllocation != nil {
		result.Arm = Empty
	}
	return result, nil
}
func selectLive(scope Scope, s Selection) (Selection, error) {
	if s.Baseline != nil {
		return Selection{}, refuse(StoppedSourceChanged)
	}
	if !scope.ReplicaCommitted {
		return Selection{}, refuse(LocalUncertain)
	}
	s.Mode = SupersedeLive
	s.Arm = LTX
	s.Source = nil
	s.CoverageOrigin = ""
	return s, nil
}
func sourceArm(scope Scope, s RemoteSource) Arm {
	if scope.EmptyAllocation != nil {
		return Empty
	}
	if s.Point.LTX != nil {
		return LTX
	}
	return Snapshot
}
