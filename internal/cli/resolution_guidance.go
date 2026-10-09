package cli

import (
	"encoding/json"
	"strings"

	"github.com/ShaulLavo/brine/internal/ops"
)

// This is journal eligibility, not a live-state safety assessment. Status only
// offers inspection for a complete supported prefix; diagnose has no prefix
// evidence and therefore directs the operator to status instead.
func resolutionPrefixSupported(op ops.Operation, events []ops.Event) bool {
	if op.State != ops.RecoveryRequired || op.Kind != ops.Deploy && op.Kind != ops.SecretSet && op.Kind != ops.Resolve {
		return false
	}
	if op.Kind == ops.SecretSet || op.Kind == ops.Resolve && op.PlanID == "" {
		name := ""
		for _, event := range events {
			if event.Kind != "secret_version" {
				continue
			}
			var version ops.SecretVersionPayload
			if ops.ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &version) != nil || !strings.HasPrefix(version.Name, "brine."+op.App+"."+op.SecretRef+".v") || name != "" && name != version.Name {
				return false
			}
			name = version.Name
		}
		return name != ""
	}
	paths := [][]string{
		{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "check_direct", "publish_route", "check_routed", "commit"},
		{"preflight", "withdraw_route", "stop_unit", "remove_unit", "reload_units", "retire_app"},
	}
	for _, path := range paths {
		index := 0
		last := ""
		valid := true
		for _, event := range events {
			if event.Kind != "step" {
				continue
			}
			var step ops.StepPayload
			if ops.ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &step) != nil || index >= len(path) || step.Step != path[index] {
				valid = false
				break
			}
			last = step.Step
			switch step.Outcome {
			case "completed":
				index++
			case "intent", "unknown":
			default:
				valid = false
			}
		}
		if valid && last != "" && last != "publish_route" && last != "check_routed" {
			return true
		}
	}
	return false
}
