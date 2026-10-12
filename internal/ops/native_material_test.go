package ops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativematerial"
)

func TestNativeMaterialOperationEventsBindReceiptAndStaySynchronous(t *testing.T) {
	planID := "sha256:" + strings.Repeat("a", 64)
	intent := Intent{Kind: NativeMaterialSet, App: "game", SecretRef: planID}
	if !ValidIntent(intent) || NativeMaterialSet.IsTask() || !CanTransitionFor(NativeMaterialSet, Queued, Preparing) || CanTransitionFor(NativeMaterialSet, Queued, Preflight) {
		t.Fatal("native material became a detached task")
	}
	op := Operation{ID: "material-operation", Kind: NativeMaterialSet, App: "game", SecretRef: planID, State: Succeeded}
	role := func(ref string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: ref, Version: 1, SHA256: strings.Repeat("a", 64)}
	}
	secrets := data.EngineSecrets{Publisher: role("publisher"), SigningPrivate: role("private"), SigningPublic: role("public"), AuthPolicy: role("auth")}
	secrets.Publisher.ExpiresAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	receipt := nativematerial.Receipt{OperationID: op.ID, App: op.App, PreparationPlanID: planID, BindingID: data.ReplicaBindingID(strings.Repeat("b", 32)), EpochID: data.ReplicaEpochID(strings.Repeat("c", 32)), Outcome: "configured_unpublished", Secrets: secrets}
	raw, _ := json.Marshal(receipt)
	if err := ValidateOperationEvent(op, Event{Kind: "native_material", Payload: raw}); err != nil {
		t.Fatal("real receipt refused", err)
	}
	for _, failure := range []string{"operation", "app", "plan", "outcome"} {
		bad := receipt
		switch failure {
		case "operation":
			bad.OperationID = "other-material"
		case "app":
			bad.App = "other-game"
		case "plan":
			bad.PreparationPlanID = "sha256:" + strings.Repeat("d", 64)
		case "outcome":
			bad.Outcome = "recovery_required"
		}
		raw, _ := json.Marshal(bad)
		if err := ValidateOperationEvent(op, Event{Kind: "native_material", Payload: raw}); err == nil {
			t.Fatal("foreign material receipt joined operation", failure)
		}
	}
	if err := ValidateOperationEvent(op, Event{Kind: "step", Payload: json.RawMessage(`{"step":"engine_start","outcome":"completed"}`)}); err == nil {
		t.Fatal("synchronous delivery acquired engine start event")
	}
}
