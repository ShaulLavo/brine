package plan

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/target"
)

// DeploymentReviewHash binds every non-data release decision. Only the data
// observations and incarnation reservations produced by the reviewed workflow
// are excluded: their immutable plans and host admission validate them separately.
func DeploymentReviewHash(in Input) (string, error) {
	desired, err := in.Desired.CanonicalBytes()
	if err != nil {
		return "", err
	}
	snapshot, err := target.Encode(in.Snapshot)
	if err != nil {
		return "", err
	}
	if err = json.Unmarshal(snapshot, &in.Snapshot); err != nil {
		return "", err
	}
	in.Snapshot.PersistentData = nil
	in.State.DataIncarnations = nil
	facts, err := canonicalDecisionFacts(in)
	if err != nil {
		return "", err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Desired  json.RawMessage
		Snapshot json.RawMessage
		State    json.RawMessage
		Image    Image
	}{desired, facts, state, in.Image})
	if err != nil {
		return "", err
	}
	return hash(raw), nil
}
