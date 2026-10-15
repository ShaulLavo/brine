package nativecapture

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type PreparationReleaseReceipt struct {
	Kind            string `json:"kind"`
	OperationID     string `json:"operation_id"`
	BindingID       string `json:"binding_id"`
	EpochID         string `json:"epoch_id"`
	PointID         string `json:"point_id"`
	FenceID         string `json:"fence_id"`
	DesiredRevision uint64 `json:"desired_revision"`
	RouteRevision   uint64 `json:"route_revision"`
	Outcome         string `json:"outcome"`
}

func DecodePreparationReleaseReceipt(raw []byte) (PreparationReleaseReceipt, error) {
	var r PreparationReleaseReceipt
	if len(raw) == 0 || len(raw) > 4096 {
		return r, ErrLimit
	}
	if _, err := strictjson.Object(raw, "kind", "operation_id", "binding_id", "epoch_id", "point_id", "fence_id", "desired_revision", "route_revision", "outcome"); err != nil {
		return r, ErrInvalid
	}
	if json.Unmarshal(raw, &r) != nil || r.Kind != "native_prepare_release" || r.Outcome != "released" || !taskOperationID.MatchString(r.OperationID) || !idPattern.MatchString(r.BindingID) || !idPattern.MatchString(r.EpochID) || !idPattern.MatchString(r.PointID) || !idPattern.MatchString(r.FenceID) || r.DesiredRevision == 0 || r.DesiredRevision > 1<<63-1 || r.RouteRevision == 0 || r.RouteRevision > 1<<63-1 {
		return r, ErrInvalid
	}
	return r, nil
}
