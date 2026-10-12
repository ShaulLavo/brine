package ops

import (
	"encoding/json"
	"testing"
)

func TestModulePublicationResolutionCannotInventAnUnadmittedTransition(t *testing.T) {
	receipt := publicationTaskReceiptFixture()
	receipt.Kind, receipt.OperationID, receipt.Outcome = string(ModulePublishResolve), "resolve-module", "source_preserved"
	receipt.NativeDatabase, receipt.NativePublisher, receipt.ProgramWireHash, receipt.WasmSHA256, receipt.SchemaSHA256 = "", "", "", "", ""
	receipt.TransitionState, receipt.TransitionID, receipt.TransitionSHA256 = "not_admitted", "", ""
	op := Operation{ID: receipt.OperationID, Kind: ModulePublishResolve, App: "game", SecretRef: receipt.PlanID, RecoveryOf: receipt.ApplyOperationID, State: Succeeded}
	raw, _ := json.Marshal(receipt)
	if _, err := DecodeTaskReceipt(op, raw); err != nil {
		t.Fatal("truthful pretransition source-preserved resolution refused", err)
	}
	receipt.TransitionID = publicationTaskReceiptFixture().TransitionID
	raw, _ = json.Marshal(receipt)
	if _, err := DecodeTaskReceipt(op, raw); err == nil {
		t.Fatal("unadmitted resolution invented a transition")
	}
	receipt.TransitionState = "abandoned"
	raw, _ = json.Marshal(receipt)
	if _, err := DecodeTaskReceipt(op, raw); err == nil {
		t.Fatal("abandonment without an actual transition hash admitted")
	}
	receipt.TransitionSHA256 = publicationTaskReceiptFixture().TransitionSHA256
	raw, _ = json.Marshal(receipt)
	if _, err := DecodeTaskReceipt(op, raw); err != nil {
		t.Fatal("actual abandoned transition reference refused", err)
	}
}
