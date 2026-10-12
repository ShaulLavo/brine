package liverestore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type Stage string

const (
	Claimed          Stage = "claimed"
	RestoreIntent    Stage = "restore_intent"
	Verified         Stage = "verified"
	TransitionIntent Stage = "transition_intent"
	Transitioned     Stage = "transitioned"
	FinishIntent     Stage = "finish_intent"
	Completed        Stage = "completed"
	AbandonIntent    Stage = "abandon_intent"
	AbandonedCurrent Stage = "abandoned_current"
)

type Attempt struct {
	PlanID          string                `json:"plan_id"`
	OperationID     string                `json:"operation_id"`
	ConsumerSHA256  string                `json:"consumer_sha256"`
	PreparedPointID string                `json:"prepared_point_id"`
	SelectedPointID string                `json:"selected_point_id"`
	FenceID         data.FenceID          `json:"fence_id"`
	FenceOwnerOpID  string                `json:"fence_owner_op_id"`
	BindingID       data.ReplicaBindingID `json:"binding_id"`
	SourceEpochID   data.ReplicaEpochID   `json:"source_epoch_id"`
	IncomingEpochID data.ReplicaEpochID   `json:"incoming_epoch_id"`
}

func (a Attempt) matches(p Plan) bool {
	s := p.spec
	return planID(p.id) && a.PlanID == p.id && operationID.MatchString(a.OperationID) && a.OperationID != s.Safety.FenceOwnerOpID &&
		digest(a.ConsumerSHA256) && a.PreparedPointID == s.Safety.PointID && a.SelectedPointID == s.Selected.PointID &&
		a.FenceID == s.Safety.FenceID && a.FenceOwnerOpID == s.Safety.FenceOwnerOpID && a.BindingID == s.Facts.Head.BindingID &&
		a.SourceEpochID == s.Facts.Head.EpochID && data.ValidID(string(a.IncomingEpochID)) && a.IncomingEpochID != a.SourceEpochID && a.IncomingEpochID != s.Selected.EpochID
}

type eventRecord struct {
	Format         string  `json:"format"`
	Attempt        Attempt `json:"attempt"`
	Sequence       uint64  `json:"sequence"`
	Stage          Stage   `json:"stage"`
	PreviousSHA256 string  `json:"previous_sha256"`
	ProofSHA256    string  `json:"proof_sha256"`
}

// Event orders one retained attempt. Protected store readers authenticate each
// proof before settling it; an Event is never a host mutation capability.
type Event struct {
	record eventRecord
	raw    []byte
	hash   string
}

func BeginAttempt(p Plan, attempt Attempt) (Event, error) {
	if !attempt.matches(p) {
		return Event{}, ErrRecord
	}
	return encodeEvent(eventRecord{Format: "brine-native-live-restore-event-v1", Attempt: attempt, Sequence: 1, Stage: Claimed,
		PreviousSHA256: stringsHash(p.id), ProofSHA256: attempt.ConsumerSHA256})
}

func (e Event) Attempt() Attempt { return e.record.Attempt }
func (e Event) Stage() Stage     { return e.record.Stage }
func (e Event) Sequence() uint64 { return e.record.Sequence }
func (e Event) SHA256() string   { return e.hash }
func (e Event) Bytes() []byte    { return bytes.Clone(e.raw) }
func (e Event) Pending() bool {
	return e.record.Stage == RestoreIntent || e.record.Stage == TransitionIntent || e.record.Stage == FinishIntent || e.record.Stage == AbandonIntent
}
func (e Event) Terminal() bool {
	return e.record.Stage == Completed || e.record.Stage == AbandonedCurrent
}

// RecordIntent never repeats a pending action. Reconciliation settles the
// original intent through observed protected evidence instead of retrying it.
func RecordIntent(p Plan, old Event, intent Stage) (Event, error) {
	if !old.record.Attempt.matches(p) || old.hash == "" || old.Pending() || old.Terminal() || !nextIntent(old.record.Stage, intent) {
		return Event{}, ErrRecord
	}
	return successor(old, intent, "")
}

// Settle records positively observed evidence for the original pending action.
// The operation adapter must join the actual protected proof in the same tx.
func Settle(p Plan, old Event, completed Stage, proofSHA256 string) (Event, error) {
	if !old.record.Attempt.matches(p) || old.hash == "" || !old.Pending() || !digest(proofSHA256) || !settles(old.record.Stage, completed) {
		return Event{}, ErrRecord
	}
	return successor(old, completed, proofSHA256)
}

func nextIntent(old, next Stage) bool {
	if next == AbandonIntent {
		return old == Claimed || old == Verified
	}
	return old == Claimed && next == RestoreIntent || old == Verified && next == TransitionIntent || old == Transitioned && next == FinishIntent
}

func settles(intent, completed Stage) bool {
	return intent == RestoreIntent && completed == Verified ||
		intent == TransitionIntent && completed == Transitioned || intent == FinishIntent && completed == Completed ||
		intent == AbandonIntent && completed == AbandonedCurrent
}

func successor(old Event, stage Stage, proof string) (Event, error) {
	if old.record.Sequence >= math.MaxInt64 {
		return Event{}, ErrRecord
	}
	r := old.record
	r.Sequence, r.Stage, r.PreviousSHA256, r.ProofSHA256 = r.Sequence+1, stage, old.hash, proof
	return encodeEvent(r)
}

func encodeEvent(r eventRecord) (Event, error) {
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 4096 {
		return Event{}, ErrRecord
	}
	sum := sha256.Sum256(raw)
	return Event{record: r, raw: raw, hash: hex.EncodeToString(sum[:])}, nil
}

func stringsHash(id string) string { return id[len("sha256:"):] }

func DecodeEvent(p Plan, previous *Event, raw []byte) (Event, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return Event{}, ErrRecord
	}
	fields, err := strictjson.Object(raw, "format", "attempt", "sequence", "stage", "previous_sha256", "proof_sha256")
	if err != nil {
		return Event{}, ErrRecord
	}
	if _, err = strictjson.Object(fields["attempt"], "plan_id", "operation_id", "consumer_sha256", "prepared_point_id", "selected_point_id", "fence_id", "fence_owner_op_id", "binding_id", "source_epoch_id", "incoming_epoch_id"); err != nil {
		return Event{}, ErrRecord
	}
	var r eventRecord
	if json.Unmarshal(raw, &r) != nil {
		return Event{}, ErrRecord
	}
	derived, err := deriveEvent(p, previous, r)
	if err != nil || !bytes.Equal(derived.raw, raw) {
		return Event{}, ErrRecord
	}
	return derived, nil
}

func deriveEvent(p Plan, old *Event, r eventRecord) (Event, error) {
	if old == nil {
		return BeginAttempt(p, r.Attempt)
	}
	if r.Attempt != old.record.Attempt {
		return Event{}, ErrRecord
	}
	if r.ProofSHA256 == "" {
		return RecordIntent(p, *old, r.Stage)
	}
	return Settle(p, *old, r.Stage, r.ProofSHA256)
}
