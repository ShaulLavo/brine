package sqliteprep

import (
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

// SnapshotPlan is the sealed capture identity persisted before one upload
// invocation. The host retains and clears the bytes; none enter this package.
type SnapshotPlan struct {
	Point  data.SQLiteRestorePoint `json:"point"`
	Cutoff time.Time               `json:"cutoff"`
}

func (p SnapshotPlan) Validate() error {
	if p.Point.Validate() != nil || p.Point.Kind != data.RestorePointSnapshot || p.Point.Snapshot.Size > MaxCaptureBytes || !utc(p.Cutoff) || !utc(p.Point.RecordedAt) || !utc(p.Point.Schema.ObservedAt) || p.Point.RecordedAt.Before(p.Cutoff) {
		return ErrInvalid
	}
	return nil
}

type Event struct {
	PreparationID  string                 `json:"preparation_id"`
	Sequence       int                    `json:"sequence"`
	Stage          Stage                  `json:"stage"`
	At             time.Time              `json:"at"`
	Stopped        *StoppedBundle         `json:"stopped,omitempty"`
	Snapshot       *SnapshotPlan          `json:"snapshot,omitempty"`
	Source         *RemoteSource          `json:"source,omitempty"`
	Verification   *restore.SQLiteReceipt `json:"verification,omitempty"`
	PreparedSHA256 string                 `json:"prepared_sha256,omitempty"`
}

func (e Event) Validate() error {
	if !data.ValidID(e.PreparationID) || e.Sequence < 1 || e.Sequence > 10 || !utc(e.At) {
		return ErrInvalid
	}
	payloads := 0
	for _, present := range []bool{e.Stopped != nil, e.Snapshot != nil, e.Source != nil, e.Verification != nil, e.PreparedSHA256 != ""} {
		if present {
			payloads++
		}
	}
	switch e.Stage {
	case Claimed, WritersStopIntent, WritersStopped, ReplicaStopIntent:
		if payloads != 0 {
			return ErrInvalid
		}
	case ReplicaStopped:
		if payloads != 1 || e.Stopped == nil || e.Stopped.Validate() != nil {
			return ErrInvalid
		}
	case CoverageIntent:
		if payloads != 1 || e.Snapshot == nil || e.Snapshot.Validate() != nil || e.Snapshot.Point.RecordedAt.After(e.At) {
			return ErrInvalid
		}
	case CoverageUploaded, CoverageRetained:
		if payloads != 1 || e.Source == nil || e.Source.Validate() != nil || e.Source.Point.RecordedAt.After(e.At) {
			return ErrInvalid
		}
	case Verified:
		if payloads != 1 || e.Verification == nil || !e.Verification.Valid() || e.Verification.DatabasePath != "" || !utc(e.Verification.ObservedAt) || e.Verification.ObservedAt.After(e.At) {
			return ErrInvalid
		}
	case Prepared:
		if payloads != 1 || !data.ValidCatalogHash(e.PreparedSHA256) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type History struct {
	Intent Intent  `json:"intent"`
	Events []Event `json:"events"`
}

func (h History) Validate() error {
	if h.Intent.Validate() != nil || len(h.Events) == 0 || len(h.Events) > 10 {
		return ErrInvalid
	}
	for n, e := range h.Events {
		if err := h.validateEvent(n, e); err != nil {
			return err
		}
	}
	return validateEvidence(h)
}
func (h History) validateEvent(n int, e Event) error {
	if e.Validate() != nil || e.PreparationID != h.Intent.Input.PreparationID || e.Sequence != n+1 || e.At.Before(h.Intent.ClaimedAt) {
		return ErrInvalid
	}
	if n == 0 {
		if e.Stage != Claimed || !e.At.Equal(h.Intent.ClaimedAt) {
			return ErrInvalid
		}
		return nil
	}
	prior := h.Events[n-1]
	if !Next(h.Intent.Mode, h.Intent.Arm, prior.Stage, e.Stage) || e.At.Before(prior.At) {
		return ErrInvalid
	}
	if prior.Stage == Claimed && (e.Stage == WritersStopIntent) != h.Intent.StopWritersRequired {
		return ErrInvalid
	}
	return nil
}
func validateEvidence(h History) error {
	var source *RemoteSource
	var snapshot *SnapshotPlan
	var cutoff time.Time
	for _, e := range h.Events {
		if e.Stage == WritersStopped {
			cutoff = e.At
		}
		if e.Snapshot != nil && !snapshotMatches(h.Intent, *e.Snapshot, cutoff) {
			return ErrInvalid
		}
		if e.Snapshot != nil {
			snapshot = e.Snapshot
		}
		if e.Stopped != nil && !bundleForIntent(h.Intent, *e.Stopped) {
			return ErrInvalid
		}
		if e.Source != nil && !eventSourceMatches(h.Intent, *e.Source, snapshot, cutoff) {
			return ErrInvalid
		}
		if e.Source != nil {
			source = e.Source
		}
		if e.Verification != nil && (source == nil || !verificationMatches(h.Intent, *source, *e.Verification)) {
			return ErrInvalid
		}
	}
	return nil
}
func snapshotMatches(i Intent, p SnapshotPlan, cutoff time.Time) bool {
	return p.Validate() == nil && p.Point.ID == i.Input.PreparationID && p.Point.BindingID == i.Scope.BindingID && p.Point.EpochID == i.Scope.EpochID && p.Point.Snapshot.ObjectKey == SnapshotKey(i.Scope, i.Input.PreparationID) && sameSchema(p.Point.Schema, pointSchema(i.Scope.Schema)) && p.Cutoff.Equal(cutoff)
}
func eventSourceMatches(i Intent, s RemoteSource, p *SnapshotPlan, cutoff time.Time) bool {
	if !s.MatchesScope(i.Scope) || (s.Point.Kind == data.RestorePointLTX) != (i.Arm == LTX) {
		return false
	}
	if i.Mode == SupersedeReverify {
		return s.Origin == i.CoverageOrigin
	}
	if s.Origin != i.Input.PreparationID || !s.Cutoff.Equal(cutoff) || s.UploadedAt.Before(i.ClaimedAt) {
		return false
	}
	if i.Arm == LTX {
		return s.Point.Kind == data.RestorePointLTX
	}
	return p != nil && s.Point.Kind == data.RestorePointSnapshot && p.Point.ID == s.Point.ID && p.Point.BindingID == s.Point.BindingID && p.Point.EpochID == s.Point.EpochID && reflect.DeepEqual(p.Point.Snapshot, s.Point.Snapshot) && reflect.DeepEqual(p.Point.Schema, s.Point.Schema) && p.Cutoff.Equal(s.Cutoff) && !s.UploadedAt.Before(p.Point.RecordedAt)
}
func bundleForIntent(i Intent, b StoppedBundle) bool {
	if b.Validate() != nil || b.Location != i.Location {
		return false
	}
	if i.Scope.EmptyAllocation != nil {
		return b.DB.Presence == Absent
	}
	return b.DB.Presence == Regular
}
func verificationMatches(i Intent, s RemoteSource, r restore.SQLiteReceipt) bool {
	if !s.MatchesScope(i.Scope) || !receiptMatchesSource(s, r) || r.OperationID != i.Task.OperationID || r.ObservedAt.Before(i.ClaimedAt) {
		return false
	}
	// No declaration means unknown, not a promoted invariant pass.
	if len(i.Scope.Checks) == 0 {
		return r.InvariantCheck == "unknown"
	}
	return r.InvariantCheck == "passed"
}
func receiptMatchesSource(s RemoteSource, r restore.SQLiteReceipt) bool {
	schema := s.Point.Schema
	expected := restore.SQLiteSchemaObservation{State: restore.SchemaState(schema.State), Marker: schema.Marker, CatalogSHA256: schema.CatalogSHA256}
	return r.Valid() && r.DatabasePath == "" && reflect.DeepEqual(r.Source, s.RestoreSource()) && r.Schema == expected && utc(r.ObservedAt) && !r.ObservedAt.Before(s.Point.RecordedAt) && r.Sentinel == nil && r.Coverage != nil && r.Coverage.CoveredThrough.Equal(s.Cutoff) && reflect.DeepEqual(r.Coverage.Source, s.RestoreSource()) && r.LossWindow.State == restore.LossBounded
}

type PreparedPoint struct {
	PreparationID string                `json:"preparation_id"`
	IntentSHA256  string                `json:"intent_sha256"`
	Source        RemoteSource          `json:"source"`
	Verification  restore.SQLiteReceipt `json:"verification"`
	Stopped       StoppedBundle         `json:"stopped"`
	PreparedAt    time.Time             `json:"prepared_at"`
	ExpiresAt     time.Time             `json:"expires_at"`
}

func (p PreparedPoint) Validate() error {
	if !data.ValidID(p.PreparationID) || !data.ValidCatalogHash(p.IntentSHA256) || p.Source.Validate() != nil || !receiptMatchesSource(p.Source, p.Verification) || p.Stopped.Validate() != nil || !utc(p.PreparedAt) || !utc(p.ExpiresAt) || !utc(p.Verification.ObservedAt) || p.Verification.ObservedAt.Before(p.Source.UploadedAt) || p.PreparedAt.Before(p.Verification.ObservedAt) || !p.PreparedAt.Before(p.ExpiresAt) || p.ExpiresAt.Sub(p.PreparedAt) > PointLifetime {
		return ErrInvalid
	}
	return nil
}

// ValidatePrepared checks the whole mode-specific path, not a universal final
// stage. Real completion must additionally join protected task, scope, origin,
// declarations, fence and held source authority in the same store transaction.
func ValidatePrepared(h History, p PreparedPoint) error {
	if h.Validate() != nil || p.Validate() != nil || h.Events[len(h.Events)-1].Stage != finalStage(h.Intent) || p.PreparationID != h.Intent.Input.PreparationID || p.ExpiresAt.After(h.Intent.Scope.Retention.RetainUntil) || !verificationMatches(h.Intent, p.Source, p.Verification) || !bundleForIntent(h.Intent, p.Stopped) {
		return ErrInvalid
	}
	digest, err := Fingerprint(h.Intent)
	if err != nil || p.IntentSHA256 != digest {
		return ErrInvalid
	}
	var source *RemoteSource
	var stopped *StoppedBundle
	var verified *restore.SQLiteReceipt
	for _, e := range h.Events {
		if e.Source != nil {
			source = e.Source
		}
		if e.Stopped != nil {
			stopped = e.Stopped
		}
		if e.Verification != nil {
			verified = e.Verification
		}
	}
	if source == nil || stopped == nil || verified == nil || !sameSource(*source, p.Source) || !reflect.DeepEqual(*verified, p.Verification) || !stopped.Matches(p.Stopped) || p.PreparedAt.Before(h.Events[len(h.Events)-1].At) || !p.PreparedAt.Before(h.Intent.Scope.Retention.FreshUntil) {
		return ErrInvalid
	}
	return nil
}

type Outcome string

const (
	PreparedOutcome  Outcome = "prepared"
	AbandonedOutcome Outcome = "abandoned_stopped"
)

type Receipt struct {
	OldObject              ObjectState          `json:"old_object,omitempty"`
	Task                   Task                 `json:"task"`
	PreparationID          string               `json:"preparation_id"`
	PreparedSHA256         string               `json:"prepared_sha256"`
	Fence                  data.QuiescenceFence `json:"fence"`
	SourcePointID          string               `json:"source_point_id"`
	Outcome                Outcome              `json:"outcome"`
	StoppedDesiredRevision uint64               `json:"stopped_desired_revision"`
	CompletedAt            time.Time            `json:"completed_at"`
}

func (r Receipt) Validate() error {
	if r.Task.Validate() != nil || !data.ValidID(r.PreparationID) || !data.ValidID(string(r.Fence.ID)) || !data.ValidID(string(r.Fence.DatabaseID)) || !data.ValidID(string(r.Fence.IncarnationID)) || !token.MatchString(r.Fence.OperationID) || !utc(r.CompletedAt) {
		return ErrInvalid
	}
	switch r.Outcome {
	case PreparedOutcome:
		if r.OldObject != "" || !data.ValidID(r.SourcePointID) || !data.ValidCatalogHash(r.PreparedSHA256) || r.Task.Kind != PrepareTask || r.Fence.State != data.FenceHeld || r.StoppedDesiredRevision != 0 {
			return ErrInvalid
		}
	case AbandonedOutcome:
		if (r.SourcePointID != "" || r.PreparedSHA256 != "") && (!data.ValidID(r.SourcePointID) || !data.ValidCatalogHash(r.PreparedSHA256)) {
			return ErrInvalid
		}
		if r.OldObject != ObjectUnknown && r.OldObject != ObjectPresent && r.OldObject != ObjectAbsent {
			return ErrInvalid
		}
		if r.Task.Kind != AbandonTask || r.Fence.State != data.FenceReleased || r.StoppedDesiredRevision == 0 || r.StoppedDesiredRevision > 1<<63-1 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type ConsumerKind string

const (
	SupersedeConsumer ConsumerKind = "supersede"
	AbandonConsumer   ConsumerKind = "abandon"
	ReviewedConsumer  ConsumerKind = "reviewed_mutation"
)

// Consumer is an immutable claim, not generic fence-release authority. For a
// reviewed consumer the store binds its real task and plan, current policy and
// retention evidence, and keeps the source until RecoverUntil. Completion is a
// separate immutable record; no mutable Active flag stands in for that join.
type Consumer struct {
	PreparationID      string                     `json:"preparation_id"`
	OperationID        string                     `json:"operation_id"`
	Requester          string                     `json:"requester"`
	App                string                     `json:"app"`
	InputSHA256        string                     `json:"input_sha256"`
	Kind               ConsumerKind               `json:"kind"`
	SuccessorID        string                     `json:"successor_id,omitempty"`
	PreparedSHA256     string                     `json:"prepared_sha256,omitempty"`
	ReviewedPlanSHA256 string                     `json:"reviewed_plan_sha256,omitempty"`
	PolicySHA256       string                     `json:"policy_sha256"`
	RetentionSHA256    string                     `json:"retention_sha256,omitempty"`
	Bounds             *data.InitializationBounds `json:"bounds,omitempty"`
	ClaimedAt          time.Time                  `json:"claimed_at"`
	RecoverUntil       time.Time                  `json:"recover_until,omitempty"`
}

func (c Consumer) Validate() error {
	if c.PreparedSHA256 != "" && !data.ValidCatalogHash(c.PreparedSHA256) {
		return ErrInvalid
	}
	if !data.ValidID(c.PreparationID) || !token.MatchString(c.OperationID) || !token.MatchString(c.Requester) || !appName.MatchString(c.App) || !data.ValidCatalogHash(c.InputSHA256) || !data.ValidCatalogHash(c.PolicySHA256) || !utc(c.ClaimedAt) {
		return ErrInvalid
	}
	switch c.Kind {
	case SupersedeConsumer:
		if !data.ValidID(c.SuccessorID) || c.SuccessorID == c.PreparationID || c.ReviewedPlanSHA256 != "" || c.RetentionSHA256 != "" || c.Bounds != nil || !c.RecoverUntil.IsZero() {
			return ErrInvalid
		}
	case AbandonConsumer:
		if c.SuccessorID != "" || c.ReviewedPlanSHA256 != "" || c.RetentionSHA256 != "" || c.Bounds != nil || !c.RecoverUntil.IsZero() {
			return ErrInvalid
		}
	case ReviewedConsumer:
		if !data.ValidCatalogHash(c.PreparedSHA256) || c.SuccessorID != "" || !data.ValidCatalogHash(c.ReviewedPlanSHA256) || !data.ValidCatalogHash(c.RetentionSHA256) || c.Bounds == nil || !c.Bounds.Valid() || !utc(c.RecoverUntil) || !c.RecoverUntil.Equal(c.ClaimedAt.Add(time.Duration(c.Bounds.RecoveryWindowSeconds)*time.Second)) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Admits combines the immutable point with a separately joined current scope.
// The scope value grants no authority; the store must validate that join.
func (c Consumer) Admits(i Intent, p PreparedPoint, current Scope, now time.Time) bool {
	if current.Validate() != nil || !sameLineageScope(i.Scope, current) || c.PolicySHA256 != current.PolicySHA256 || c.RetentionSHA256 != current.Retention.EvidenceSHA256 || current.Schema.ObservedAt.After(now) || current.Retention.ObservedAt.After(now) {
		return false
	}
	if c.Validate() != nil || c.Kind != ReviewedConsumer || i.Validate() != nil || p.Validate() != nil || !verificationMatches(i, p.Source, p.Verification) || p.PreparationID != i.Input.PreparationID || c.PreparationID != p.PreparationID || c.App != i.Scope.App || c.Requester != i.Task.Requester || !c.ClaimedAt.Equal(now) || !utc(now) || !now.Before(p.ExpiresAt) || !now.Before(current.Retention.FreshUntil) || c.RecoverUntil.After(current.Retention.RetainUntil) {
		return false
	}
	intentHash, err := Fingerprint(i)
	pointHash, pointErr := Fingerprint(p)
	return err == nil && pointErr == nil && c.PreparedSHA256 == pointHash && p.IntentSHA256 == intentHash && !p.PreparedAt.After(now) && !p.Source.UploadedAt.After(now) && !p.Verification.ObservedAt.After(now) && now.Sub(p.Source.Cutoff) <= time.Duration(c.Bounds.MaxBackupAgeSeconds)*time.Second && now.Sub(p.Verification.ObservedAt) <= time.Duration(c.Bounds.MaxRestoreTestAgeSeconds)*time.Second
}

// ValidateCompletion is the structural half of joined task readback. The real
// store still must prove every protected join described by Journal.
func ValidateCompletion(h History, p PreparedPoint, r Receipt) error {
	if h.Validate() != nil || len(h.Events) < 2 || h.Events[len(h.Events)-1].Stage != Prepared {
		return ErrInvalid
	}
	prefix := h
	prefix.Events = h.Events[:len(h.Events)-1]
	if ValidatePrepared(prefix, p) != nil || r.Validate() != nil {
		return ErrInvalid
	}
	digest, err := Fingerprint(p)
	if err != nil || h.Events[len(h.Events)-1].PreparedSHA256 != digest || r.Task != h.Intent.Task || r.Outcome != PreparedOutcome || r.PreparationID != p.PreparationID || r.PreparedSHA256 != digest || r.SourcePointID != p.Source.Point.ID || r.Fence != h.Intent.Fence || r.CompletedAt.Before(p.PreparedAt) {
		return ErrInvalid
	}
	return nil
}
