package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeBackupResolutionDispatchRefusesPathsKeysAndEvidence(t *testing.T) {
	for _, operation := range []string{"native_backup_inspect", "native_backup_review"} {
		codec := operations[operation].decode
		if _, err := codec([]byte(`{"operation_id":"original"}`)); err != nil {
			t.Fatal(operation, err)
		}
		for _, raw := range []string{`{"operation_id":"../secret"}`, `{"operation_id":"original","source":"/private"}`, `{"operation_id":"original","force":true}`, `{"operation_id":null}`} {
			if _, err := codec([]byte(raw)); err == nil {
				t.Fatal(operation, "raw authority admitted")
			}
		}
	}
	raw, _ := json.Marshal(NativeBackupResolveArgs{ReviewID: strings.Repeat("1", 64), IdempotencyKey: strings.Repeat("2", 32)})
	if _, err := decodeNativeBackupResolve(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeBackupResolve([]byte(strings.Replace(string(raw), `"review_id":`, `"repository":"s3:unrelated","review_id":`, 1))); err == nil {
		t.Fatal("raw repository admitted")
	}
}
func TestNativeBackupInspectionReportsOnlyQualifiedUnitStates(t *testing.T) {
	view := NativeBackupInspectionView{OperationID: "original-operation", Kind: "native_backup_activate", BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), ObservationSHA256: strings.Repeat("c", 64), State: "verified_remote"}
	for _, state := range []string{"not_issued", "issued_absent", "verified"} {
		view.UnitsState = state
		raw, _ := json.Marshal(view)
		if _, err := DecodeNativeBackupInspectionView(raw); err != nil {
			t.Fatal("actual unit observation refused", state, err)
		}
	}
	for _, state := range []string{"", "unknown", "active", "initialized"} {
		view.UnitsState = state
		raw, _ := json.Marshal(view)
		if _, err := DecodeNativeBackupInspectionView(raw); err == nil {
			t.Fatal("unqualified unit observation accepted", state)
		}
	}
	view.Kind, view.UnitsState = "native_capture", "issued_absent"
	raw, _ := json.Marshal(view)
	if _, err := DecodeNativeBackupInspectionView(raw); err == nil {
		t.Fatal("unit observation joined to capture")
	}
}
