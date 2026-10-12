package nativecapture

import (
	"encoding/json"
	"regexp"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

var taskOperationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// TaskReceipt exposes a verified point reference, never source or credentials.
type TaskReceipt struct {
	Kind                   string `json:"kind"`
	OperationID            string `json:"operation_id"`
	BindingID              string `json:"binding_id"`
	EpochID                string `json:"epoch_id"`
	PointID                string `json:"point_id"`
	PointSHA256            string `json:"point_sha256"`
	Outcome                string `json:"outcome"`
	Engine                 string `json:"engine"`
	ModuleSHA256           string `json:"module_sha256"`
	SchemaSHA256           string `json:"schema_sha256"`
	CaptureIntervalSeconds int    `json:"capture_interval_seconds"`
	MaintenanceMillis      uint64 `json:"maintenance_millis"`
	Loss                   string `json:"loss"`
}

func DecodeTaskReceipt(raw []byte) (TaskReceipt, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return TaskReceipt{}, ErrLimit
	}
	if _, err := strictjson.Object(raw, "kind", "operation_id", "binding_id", "epoch_id", "point_id", "point_sha256", "outcome", "engine", "module_sha256", "schema_sha256", "capture_interval_seconds", "maintenance_millis", "loss"); err != nil {
		return TaskReceipt{}, ErrInvalid
	}
	var receipt TaskReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Outcome != "verified" || !taskOperationID.MatchString(receipt.OperationID) || !idPattern.MatchString(receipt.BindingID) || !idPattern.MatchString(receipt.EpochID) || !idPattern.MatchString(receipt.PointID) || !hashPattern.MatchString(receipt.PointSHA256) {
		return TaskReceipt{}, ErrInvalid
	}
	if receipt.Engine != "spacetimedb" || receipt.Loss != "unknown" || receipt.CaptureIntervalSeconds <= 0 || receipt.MaintenanceMillis > 365*24*3600*1000 {
		return TaskReceipt{}, ErrInvalid
	}
	if receipt.Kind != "native_prepare" && receipt.MaintenanceMillis != 0 {
		return TaskReceipt{}, ErrInvalid
	}
	if (receipt.ModuleSHA256 == "") != (receipt.SchemaSHA256 == "") || receipt.ModuleSHA256 != "" && (!hashPattern.MatchString(receipt.ModuleSHA256) || !hashPattern.MatchString(receipt.SchemaSHA256)) {
		return TaskReceipt{}, ErrInvalid
	}
	if receipt.Kind != "native_prepare" && receipt.ModuleSHA256 == "" {
		return TaskReceipt{}, ErrInvalid
	}
	if receipt.Kind != "native_capture" && receipt.Kind != "native_restore_test" && receipt.Kind != "native_prepare" {
		return TaskReceipt{}, ErrInvalid
	}
	return receipt, nil
}
