// Package ops defines the durable operation journal shared by storage and execution.
package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
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
		Preflight:     {Preparing, Failed, RecoveryRequired},
		Preparing:     {Quiescing, Failed, RollingBack, RecoveryRequired},
		Quiescing:     {Starting, RollingBack, RecoveryRequired},
		Starting:      {Checking, RollingBack, RecoveryRequired},
		Checking:      {Committing, RollingBack, RecoveryRequired},
		Committing:    {Succeeded, RecoveryRequired},
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
	if len(e.Payload) > MaxEventBytes || (e.State != "" && !ValidState(e.State)) {
		return ErrInvalidEvent
	}
	decode := func(v any) error {
		d := json.NewDecoder(bytes.NewReader(e.Payload))
		d.DisallowUnknownFields()
		if err := d.Decode(v); err != nil {
			return ErrInvalidEvent
		}
		if d.Decode(new(any)) != io.EOF {
			return ErrInvalidEvent
		}
		return nil
	}
	outcome := func(s string) bool { return slices.Contains([]string{"intent", "completed", "unknown"}, s) }
	switch e.Kind {
	case "state":
		if e.State == "" || len(e.Payload) != 0 {
			return ErrInvalidEvent
		}
	case "step":
		var p StepPayload
		if decode(&p) != nil || !slices.Contains([]string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "start_unit", "check_direct", "publish_route", "check_routed", "commit", "rollback_unit", "rollback_route", "rollback_start", "rollback_check"}, p.Step) || !slices.Contains([]string{"intent", "completed", "failed", "unknown"}, p.Outcome) {
			return ErrInvalidEvent
		}
		if p.Code != "" && (p.Outcome != "failed" && p.Outcome != "unknown" || !slices.Contains([]string{"drift", "digest_mismatch", "platform_mismatch", "secret_missing", "unit_invalid", "start_failed", "health_timeout", "health_failed", "route_invalid", "reload_failed", "reload_unknown", "journal_failed", "interrupted"}, p.Code)) {
			return ErrInvalidEvent
		}
	case "failure":
		var p FailurePayload
		if decode(&p) != nil || !slices.Contains([]string{"launch_failed", "launch_unknown", "executor_failed", "executor_incomplete", "recovery_required", "interrupted", "stale_plan"}, p.Code) {
			return ErrInvalidEvent
		}
	case "launch":
		var p LaunchPayload
		if decode(&p) != nil || !outcome(p.Outcome) {
			return ErrInvalidEvent
		}
	default:
		return ErrInvalidEvent
	}
	return nil
}
