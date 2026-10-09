package ops

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type Kind string

const (
	Deploy    Kind = "deploy"
	SecretSet Kind = "secret_set"
	Reconcile Kind = "reconcile"
)

// Intent contains identity and references only. Secret values cannot be stored.
type Intent struct {
	Kind      Kind
	PlanID    string
	App       string
	SecretRef string
}

var appName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var secretRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,252}$`)
var planID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidIntent(i Intent) bool {
	switch i.Kind {
	case Deploy:
		return planID.MatchString(i.PlanID) && i.App == "" && i.SecretRef == ""
	case Reconcile:
		return i.PlanID == "" && i.App == "" && i.SecretRef == ""
	case SecretSet:
		return i.PlanID == "" && appName.MatchString(i.App) && secretRef.MatchString(i.SecretRef) && len("brine."+i.App+"."+i.SecretRef+".v18446744073709551615") <= 253
	default:
		return false
	}
}
func ValidOperation(o Operation) bool {
	if !ValidState(o.State) {
		return false
	}
	if o.Kind == Reconcile {
		return ValidIntent(Intent{Kind: o.Kind, PlanID: o.PlanID, App: o.App, SecretRef: o.SecretRef}) && (o.State == Queued || o.State == LaunchUnknown || o.State == Preflight || o.State == Succeeded || o.State == Failed || o.State == RecoveryRequired)
	}
	if o.Kind == Deploy {
		return ValidIntent(Intent{Kind: o.Kind, PlanID: o.PlanID}) && o.SecretRef == "" && (o.App == "" || appName.MatchString(o.App))
	}
	if !ValidIntent(Intent{Kind: o.Kind, PlanID: o.PlanID, App: o.App, SecretRef: o.SecretRef}) {
		return false
	}
	return o.State == Queued || o.State == Preparing || o.State == Succeeded || o.State == Failed || o.State == RecoveryRequired
}
func TransitionsFor(kind Kind) map[State][]State {
	switch kind {
	case Deploy:
		return Transitions()
	case Reconcile:
		return map[State][]State{Queued: {LaunchUnknown, Preflight, Failed, RecoveryRequired}, LaunchUnknown: {Preflight, Failed, RecoveryRequired}, Preflight: {Succeeded, Failed, RecoveryRequired}}
	case SecretSet:
		return map[State][]State{Queued: {Preparing}, Preparing: {Succeeded, Failed, RecoveryRequired}}
	default:
		return map[State][]State{}
	}
}
func CanTransitionFor(kind Kind, from, to State) bool {
	for _, s := range TransitionsFor(kind)[from] {
		if s == to {
			return true
		}
	}
	return false
}

type SecretVersionPayload struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
}

func ValidSecretVersionName(name string) bool {
	parts := strings.Split(name, ".")
	if len(name) > 253 || len(parts) != 4 || parts[0] != "brine" || !appName.MatchString(parts[1]) || !secretRef.MatchString(parts[2]) || !strings.HasPrefix(parts[3], "v") {
		return false
	}
	version := strings.TrimPrefix(parts[3], "v")
	n, err := strconv.ParseUint(version, 10, 64)
	return err == nil && n > 0 && version == strconv.FormatUint(n, 10)
}

func ValidateOperationEvent(op Operation, e Event) error {
	if ValidateEvent(e) != nil {
		return ErrInvalidEvent
	}
	if op.Kind == Deploy {
		if e.Kind == "secret_version" {
			return ErrInvalidEvent
		}
		return nil
	}
	if op.Kind == Reconcile {
		if e.Kind != "launch" && e.Kind != "state" && e.Kind != "failure" {
			return ErrInvalidEvent
		}
		return nil
	}
	if op.Kind != SecretSet || e.Kind != "secret_version" && e.Kind != "state" && e.Kind != "failure" {
		return ErrInvalidEvent
	}
	if e.Kind == "secret_version" {
		var p SecretVersionPayload
		if json.Unmarshal(e.Payload, &p) != nil || !strings.HasPrefix(p.Name, "brine."+op.App+"."+op.SecretRef+".v") {
			return ErrInvalidEvent
		}
	}
	return nil
}
