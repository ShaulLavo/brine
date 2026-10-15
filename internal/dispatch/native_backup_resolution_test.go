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

func TestStoppedPreparationInspectionStateIsClosed(t *testing.T) {
	view := NativeBackupInspectionView{OperationID: "original-prepare", Kind: "native_prepare", BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), ObservationSHA256: strings.Repeat("c", 64), State: "stopped_unpublished", UnitsState: "absent"}
	raw, _ := json.Marshal(view)
	if _, err := DecodeNativeBackupInspectionView(raw); err != nil {
		t.Fatal("qualified stopped-only inspection refused", err)
	}
	for _, invalid := range []NativeBackupInspectionView{
		{OperationID: view.OperationID, Kind: "native_capture", BindingID: view.BindingID, EpochID: view.EpochID, ObservationSHA256: view.ObservationSHA256, State: view.State, UnitsState: view.UnitsState},
		{OperationID: view.OperationID, Kind: view.Kind, BindingID: view.BindingID, EpochID: view.EpochID, ObservationSHA256: view.ObservationSHA256, State: view.State, UnitsState: "unknown"},
	} {
		raw, _ := json.Marshal(invalid)
		if _, err := DecodeNativeBackupInspectionView(raw); err == nil {
			t.Fatal("unqualified stopped-only state admitted")
		}
	}
}

func TestNativePreparationUnissuedStopInspectionStates(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		view := NativeBackupInspectionView{OperationID: "prepare", Kind: "native_prepare", BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), ObservationSHA256: strings.Repeat("c", 64), State: "stop_not_issued", UnitsState: state}
		raw, _ := json.Marshal(view)
		if _, err := DecodeNativeBackupInspectionView(raw); err != nil {
			t.Fatal("known unissued engine stop refused", state, err)
		}
		view.Kind = "native_capture"
		raw, _ = json.Marshal(view)
		if _, err := DecodeNativeBackupInspectionView(raw); err == nil {
			t.Fatal("capture gained preparation release state")
		}
	}
}
