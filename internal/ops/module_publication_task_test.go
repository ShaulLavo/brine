package ops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

func publicationTaskReceiptFixture() datapublish.TaskReceipt {
	return datapublish.TaskReceipt{Format: "brine-module-publication-task-v1", Kind: string(ModulePublishApply), OperationID: "apply-module", ApplyOperationID: "apply-module", PlanID: "sha256:" + strings.Repeat("a", 64), PointID: strings.Repeat("b", 32), BindingID: strings.Repeat("c", 32), OldEpochID: strings.Repeat("d", 32), NewEpochID: strings.Repeat("e", 32), TransitionID: strings.Repeat("f", 32), TransitionSHA256: strings.Repeat("1", 64), TransitionState: "completed", CompletionSHA256: strings.Repeat("2", 64), Outcome: "published", NativeDatabase: strings.Repeat("3", 64), NativePublisher: strings.Repeat("4", 64), ProgramWireHash: strings.Repeat("5", 64), WasmSHA256: strings.Repeat("6", 64), SchemaSHA256: strings.Repeat("7", 64)}
}

func TestModulePublicationTasksUseOnlyRecordedPlanAndOriginalReferences(t *testing.T) {
	apply := Intent{Kind: ModulePublishApply, App: "game", SecretRef: "sha256:" + strings.Repeat("a", 64)}
	resolve := Intent{Kind: ModulePublishResolve, App: apply.App, SecretRef: apply.SecretRef, RecoveryOf: "apply-module"}
	if !ValidIntent(apply) || !ValidIntent(resolve) || !ModulePublishApply.IsTask() || !ModulePublishResolve.IsTask() || CanTransitionFor(ModulePublishApply, Preflight, Starting) || CanTransitionFor(ModulePublishApply, RecoveryRequired, Queued) {
		t.Fatal("publication tasks gained raw-body or replay states")
	}
	resolve.RecoveryOf = ""
	apply.SecretRef = "caller-proof"
	if ValidIntent(resolve) || ValidIntent(apply) {
		t.Fatal("missing original or unhashed plan reference accepted")
	}
}

func TestModulePublicationTaskReceiptCannotMoveAcrossOperationOrPlan(t *testing.T) {
	receipt := publicationTaskReceiptFixture()
	op := Operation{ID: receipt.OperationID, Kind: ModulePublishApply, App: "game", SecretRef: receipt.PlanID, State: Succeeded}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeTaskReceipt(op, raw); err != nil {
		t.Fatal(err)
	}
	op.ID = "another-operation"
	if _, err = DecodeTaskReceipt(op, raw); err == nil {
		t.Fatal("publication receipt crossed operation")
	}
	op.ID, op.SecretRef = receipt.OperationID, "sha256:"+strings.Repeat("9", 64)
	if _, err = DecodeTaskReceipt(op, raw); err == nil {
		t.Fatal("publication receipt crossed plan")
	}
	op = Operation{ID: "resolve-module", Kind: ModulePublishResolve, App: "game", SecretRef: receipt.PlanID, RecoveryOf: receipt.OperationID, State: Succeeded}
	receipt.Kind, receipt.OperationID = string(ModulePublishResolve), op.ID
	raw, _ = json.Marshal(receipt)
	if _, err = DecodeTaskReceipt(op, raw); err != nil {
		t.Fatal("same retained attempt could not report resolution", err)
	}
	op.RecoveryOf = "unrelated-original"
	if _, err = DecodeTaskReceipt(op, raw); err == nil {
		t.Fatal("resolution changed original apply operation")
	}
}
