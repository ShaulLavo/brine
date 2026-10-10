package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
)

func RecordPlanDrift(ctx context.Context, journal interface {
	AppendEvent(context.Context, string, Event) (uint64, error)
}, id string, before, after plan.Plan) error {
	drift, err := plan.CompareDecisionInputs(before.DecisionInput(), after.DecisionInput())
	if err != nil {
		return err
	}
	if drift.Changed == 0 {
		return fmt.Errorf("refused plans have no differing decision inputs")
	}
	payload, err := json.Marshal(drift)
	if err != nil {
		return err
	}
	event := Event{Kind: "plan_drift", Payload: payload}
	if err = ValidateEvent(event); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err = journal.AppendEvent(ctx, id, event)
	return err
}
