package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

// Compensation may reuse a closed prior lifecycle proof only through the
// immutable revision restored by the failed deployment under the same runtime.
func managedLifecycleIntent(ctx context.Context, tx *sql.Tx, head ManagedDesiredState) (ManagedDesiredState, bool, error) {
	p, _, err := loadPlan(ctx, tx, head.PlanID)
	if err != nil {
		return head, false, err
	}
	if p.Lifecycle == plan.StartApp || p.Lifecycle == plan.RestartApp {
		return head, false, nil
	}
	if p.Lifecycle != "" || p.ManagedIntentRevision == 0 || head.Revision != p.ManagedIntentRevision+2 {
		return head, false, nil
	}
	var state ops.State
	var operationPlan, app string
	if err := tx.QueryRowContext(ctx, "SELECT state,COALESCE(plan_id,''),app FROM operations WHERE id=?", head.OperationID).Scan(&state, &operationPlan, &app); err != nil {
		return head, false, err
	}
	if operationPlan != p.Hash || app != p.App || state != ops.RollingBack && state != ops.RolledBack {
		return head, false, nil
	}
	previous, err := readManagedDesiredRevision(ctx, tx, head.BindingID, p.ManagedIntentRevision)
	if err != nil || !previous.Current(head.BindingID, head.EpochID, head.RuntimeSHA256) || previous.EngineRunning != head.EngineRunning || previous.FrontendRunning != head.FrontendRunning || previous.IngressServing != head.IngressServing {
		return head, false, ErrConflict
	}
	if err := managedDesiredRestorationCompleted(ctx, tx, head.OperationID); err != nil {
		return head, false, err
	}
	return previous, true, nil
}
func managedDesiredRestorationCompleted(ctx context.Context, tx *sql.Tx, operation string) error {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM events WHERE operation_id=? AND kind='step' AND seq<COALESCE((SELECT MIN(seq) FROM events WHERE operation_id=? AND kind='state' AND state='rolled_back'),9223372036854775807) ORDER BY seq LIMIT 129", operation, operation)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	outcome, count := "", 0
	for rows.Next() {
		var raw []byte
		var step ops.StepPayload
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if ops.ValidateEvent(ops.Event{Kind: "step", Payload: raw}) != nil || json.Unmarshal(raw, &step) != nil {
			return ErrConflict
		}
		if step.Step == "managed_restore_desired" {
			outcome = step.Outcome
		}
		count++
		if count > 128 {
			return ErrConflict
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if outcome != "completed" {
		return ErrConflict
	}
	return nil
}
