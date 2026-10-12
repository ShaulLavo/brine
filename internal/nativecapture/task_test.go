package nativecapture

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeTaskReceiptClosesEngineAndObservedMaintenanceFacts(t *testing.T) {
	receipt := TaskReceipt{Kind: "native_capture", OperationID: "native-operation", BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), PointID: strings.Repeat("c", 32), PointSHA256: strings.Repeat("d", 64), Outcome: "verified", Engine: "spacetimedb", ModuleSHA256: strings.Repeat("e", 64), SchemaSHA256: strings.Repeat("f", 64), CaptureIntervalSeconds: 30, Loss: "unknown"}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTaskReceipt(raw); err != nil {
		t.Fatal("complete native receipt", err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"engine":"spacetimedb"`, `"engine":"sqlite"`, 1),
		strings.Replace(string(raw), `"loss":"unknown"`, `"loss":"zero"`, 1),
		strings.Replace(string(raw), `"maintenance_millis":0`, `"maintenance_millis":1`, 1),
		strings.Replace(string(raw), `"capture_interval_seconds":30`, `"capture_interval_seconds":0`, 1),
		strings.Replace(string(raw), `"module_sha256":"`+strings.Repeat("e", 64)+`"`, `"module_sha256":""`, 1),
	} {
		if _, err := DecodeTaskReceipt([]byte(bad)); err == nil {
			t.Fatal("receipt acquired crossed engine or invented measurement")
		}
	}
	receipt.Kind = "native_prepare"
	receipt.MaintenanceMillis = 1500
	receipt.ModuleSHA256, receipt.SchemaSHA256 = "", ""
	raw, _ = json.Marshal(receipt)
	if _, err := DecodeTaskReceipt(raw); err != nil {
		t.Fatal("actual unpublished preparation measurement", err)
	}
}
