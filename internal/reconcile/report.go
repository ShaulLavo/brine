package reconcile

import (
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func DecodeReport(raw json.RawMessage) (Report, error) {
	fields, err := strictjson.Object(raw, "dry_run", "outcomes")
	if err != nil {
		return Report{}, err
	}
	dry, err := strictjson.Value[bool](fields["dry_run"])
	if err != nil {
		return Report{}, err
	}
	raws, err := strictjson.Value[[]json.RawMessage](fields["outcomes"])
	if err != nil {
		return Report{}, err
	}
	report := Report{DryRun: dry, Outcomes: []Outcome{}}
	for _, raw := range raws {
		var outcome Outcome
		if json.Unmarshal(raw, &outcome) != nil {
			return Report{}, strictjson.ErrObject
		}
		keys := []string{"operation_id", "before", "after", "action"}
		if outcome.Code != "" {
			keys = append(keys, "code")
		}
		if outcome.Step != "" {
			keys = append(keys, "step")
		}
		fields, err := strictjson.Object(raw, keys...)
		if err != nil {
			return Report{}, err
		}
		for _, value := range fields {
			if _, err := strictjson.Value[string](value); err != nil {
				return Report{}, err
			}
		}
		if _, err := systemd.ParseOperationID(outcome.OperationID); err != nil || !ops.ValidState(outcome.Before) || !ops.ValidState(outcome.After) || !slices.Contains([]string{"running", "unchanged", "failed", "resume", "rollback", "succeeded", "recovery_required"}, outcome.Action) {
			return Report{}, strictjson.ErrObject
		}
		if outcome.Code != "" {
			payload, _ := json.Marshal(ops.FailurePayload{Code: outcome.Code})
			if ops.ValidateEvent(ops.Event{Kind: "failure", Payload: payload}) != nil {
				return Report{}, strictjson.ErrObject
			}
		}
		if outcome.Step != "" {
			payload, _ := json.Marshal(ops.StepPayload{Step: outcome.Step, Outcome: "intent"})
			if ops.ValidateEvent(ops.Event{Kind: "step", Payload: payload}) != nil {
				return Report{}, strictjson.ErrObject
			}
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	return report, nil
}
