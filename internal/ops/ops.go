// Package ops defines the durable operation journal shared by storage and execution.
package ops

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

type State string

const (
	Queued           State = "queued"
	LaunchUnknown    State = "launch_unknown"
	Preflight        State = "preflight"
	Preparing        State = "preparing"
	Quiescing        State = "quiescing"
	Starting         State = "starting"
	Checking         State = "checking"
	Committing       State = "committing"
	RollingBack      State = "rolling_back"
	Succeeded        State = "succeeded"
	Failed           State = "failed"
	RolledBack       State = "rolled_back"
	RecoveryRequired State = "recovery_required"
)

// Transitions returns a fresh table so callers cannot change the state machine.
func Transitions() map[State][]State {
	return map[State][]State{
		Queued:        {LaunchUnknown, Preflight, Failed, RecoveryRequired},
		LaunchUnknown: {Preflight, Failed, RecoveryRequired},
		Preflight:     {Preparing, Succeeded, Failed, RecoveryRequired},
		Preparing:     {Quiescing, Starting, Failed, RollingBack, RecoveryRequired},
		Quiescing:     {Starting, Succeeded, Failed, RollingBack, RecoveryRequired},
		Starting:      {Checking, Failed, RollingBack, RecoveryRequired},
		Checking:      {Committing, Succeeded, Failed, RollingBack, RecoveryRequired},
		Committing:    {Succeeded, RollingBack, RecoveryRequired},
		RollingBack:   {RolledBack, RecoveryRequired},
	}
}
func CanTransition(from, to State) bool {
	for _, s := range Transitions()[from] {
		if s == to {
			return true
		}
	}
	return false
}
func (s State) IsTerminal() bool {
	return s == Succeeded || s == Failed || s == RolledBack || s == RecoveryRequired
}

type Operation struct {
	ID             string    `json:"id"`
	RecoveryOf     string    `json:"recovery_of,omitempty"`
	Kind           Kind      `json:"kind"`
	App            string    `json:"app"`
	SecretRef      string    `json:"secret_ref"`
	PlanID         string    `json:"plan_id"`
	Requester      string    `json:"-"`
	IdempotencyKey string    `json:"-"`
	State          State     `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type Event struct {
	Sequence  uint64          `json:"sequence"`
	Kind      string          `json:"kind"`
	State     State           `json:"state,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// Release contains only immutable artifacts and secret version references.
// Its normalized desired input belongs to the referenced plan, not this record.
type Release struct {
	ID              string               `json:"id"`
	PlanID          string               `json:"plan_id"`
	Image           plan.Image           `json:"image"`
	HostPort        target.Port          `json:"host_port"`
	Secrets         []plan.SecretBinding `json:"secrets"`
	Units           []target.Unit        `json:"units"`
	CaddyFile       target.CaddyFile     `json:"caddy_file"`
	CaddyGeneration uint64               `json:"caddy_generation"`
}

const MaxEventBytes = 4096

type StepPayload struct {
	Step    string `json:"step"`
	Code    string `json:"code,omitempty"`
	Outcome string `json:"outcome"`
}
type FailurePayload struct {
	Code string `json:"code"`
}
type ResolutionPayload struct {
	OperationID string `json:"operation_id"`
}

type LaunchPayload struct {
	Outcome string `json:"outcome"`
}

var ErrInvalidEvent = errors.New("invalid journal event")

func ValidState(s State) bool {
	return s.IsTerminal() || s == LaunchUnknown || len(Transitions()[s]) != 0
}

// ValidateEvent accepts only closed, value-free schemas. Raw logs, arbitrary
// error text and literal configuration values do not belong in this journal.
func ValidateEvent(e Event) error {
	if len(e.Payload) > MaxEventBytes || (e.State != "" && (e.Kind != "state" || !ValidState(e.State))) {
		return ErrInvalidEvent
	}
	decode := func(v any, fields ...string) error {
		values, err := strictjson.Object(e.Payload, fields...)
		if err != nil {
			return ErrInvalidEvent
		}
		for _, value := range values {
			if _, err := strictjson.Value[string](value); err != nil {
				return ErrInvalidEvent
			}
		}
		if json.Unmarshal(e.Payload, v) != nil {
			return ErrInvalidEvent
		}
		return nil
	}
	outcome := func(s string) bool { return slices.Contains([]string{"intent", "completed", "unknown"}, s) }
	switch e.Kind {
	case "resolution":
		var p ResolutionPayload
		if decode(&p, "operation_id") != nil || !operationID.MatchString(p.OperationID) {
			return ErrInvalidEvent
		}
	case "secret_version":
		var p SecretVersionPayload
		if decode(&p, "name", "outcome") != nil || !ValidSecretVersionName(p.Name) || !outcome(p.Outcome) {
			return ErrInvalidEvent
		}
	case "state":
		if e.State == "" || len(e.Payload) != 0 {
			return ErrInvalidEvent
		}
	case "step":
		var p StepPayload
		if decode(&p, "step", "outcome") != nil && decode(&p, "step", "outcome", "code") != nil || !slices.Contains([]string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "stop_unit", "withdraw_route", "remove_unit", "retire_app", "start_unit", "check_direct", "publish_route", "check_routed", "commit", "rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_route", "check_compatibility", "rollback_start", "rollback_check"}, p.Step) || !slices.Contains([]string{"intent", "completed", "failed", "unknown"}, p.Outcome) {
			return ErrInvalidEvent
		}
		if p.Code != "" {
			proof := slices.Contains([]string{"stateless_compatible", "compatibility_verified"}, p.Code)
			if p.Code == "effect_refused" {
				if p.Outcome != "failed" || !slices.Contains([]string{"quiesce_old", "install_unit", "reload_units", "stop_unit", "remove_unit", "start_unit", "rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_route", "rollback_start"}, p.Step) {
					return ErrInvalidEvent
				}
			} else if proof {
				if p.Step != "check_compatibility" || p.Outcome != "completed" {
					return ErrInvalidEvent
				}
			} else if p.Outcome != "failed" && p.Outcome != "unknown" || !slices.Contains([]string{"drift", "digest_mismatch", "platform_mismatch", "secret_missing", "unit_invalid", "start_failed", "health_timeout", "health_failed", "route_invalid", "reload_failed", "reload_unknown", "journal_failed", "interrupted", "inventory_failed", "stop_failed", "unit_failed", "commit_failed", "rollback_failed", "compatibility_unknown"}, p.Code) {
				return ErrInvalidEvent
			}
		}
	case "failure":
		var p FailurePayload
		if decode(&p, "code") != nil || !slices.Contains([]string{"launch_failed", "launch_unknown", "executor_failed", "executor_incomplete", "recovery_required", "interrupted", "stale_plan", "lock_unavailable"}, p.Code) {
			return ErrInvalidEvent
		}
	case "launch":
		var p LaunchPayload
		if decode(&p, "outcome") != nil || !outcome(p.Outcome) {
			return ErrInvalidEvent
		}
	default:
		return ErrInvalidEvent
	}
	return nil
}

// Lock is the shared host-mutation exclusion handle.
type Lock interface{ Release() error }

// ErrLockUnavailable reports refusal before any protected work starts.
var ErrLockUnavailable = errors.New("operation lock unavailable")

var ErrStateConflict = errors.New("operation state conflict")

// StateConflictError reports the state observed by a refused conditional update.
type StateConflictError struct{ Current State }

func (*StateConflictError) Error() string { return "operation state conflict" }
func (*StateConflictError) Unwrap() error { return ErrStateConflict }

// OperationRecord contains a bounded, value-free diagnostic summary.
type OperationRecord struct {
	Operation   Operation `json:"operation"`
	FailureCode string    `json:"failure_code,omitempty"`
}
