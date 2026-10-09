package ops

import (
	"regexp"
	"strconv"
	"strings"
)

type Kind string

const (
	Deploy    Kind = "deploy"
	SecretSet Kind = "secret_set"
)

// Intent contains identity and references only. Secret values cannot be stored.
type Intent struct {
	Kind      Kind
	PlanID    string
	App       string
	SecretRef string
}

var appName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var secretRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,252}$`)
var planID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidIntent(i Intent) bool {
	switch i.Kind {
	case Deploy:
		return planID.MatchString(i.PlanID) && i.App == "" && i.SecretRef == ""
	case SecretSet:
		return i.PlanID == "" && appName.MatchString(i.App) && secretRef.MatchString(i.SecretRef) && len("brine-"+i.App+"-"+i.SecretRef+"-v18446744073709551615") <= 253
	default:
		return false
	}
}
func ValidOperation(o Operation) bool {
	if !ValidState(o.State) {
		return false
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
	if len(name) > 253 || !strings.HasPrefix(name, "brine-") {
		return false
	}
	stem, version, ok := strings.Cut(name, "-v")
	if !ok {
		return false
	}
	// References can themselves contain -v. Only the final delimiter is a version.
	if index := strings.LastIndex(name, "-v"); index >= 0 {
		stem, version = name[:index], name[index+2:]
	}
	n, err := strconv.ParseUint(version, 10, 64)
	if err != nil || n == 0 || version != strconv.FormatUint(n, 10) {
		return false
	}
	stem = strings.TrimPrefix(stem, "brine-")
	for index := 0; index < len(stem); index++ {
		if stem[index] == '-' && appName.MatchString(stem[:index]) && secretRef.MatchString(stem[index+1:]) {
			return true
		}
	}
	return false
}
