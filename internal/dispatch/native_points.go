package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativePointArgs struct {
	Kind    string `json:"kind"`
	PointID string `json:"point_id"`
}

func (a NativePointArgs) Valid() bool {
	return (a.Kind == "prepared" || a.Kind == "verified") && data.ValidID(a.PointID)
}

type NativePointInspector interface {
	InspectNativePoint(context.Context, NativePointArgs) (nativecapture.PointInspection, error)
}

func decodeNativePoint(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "kind", "point_id")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	kind, kindErr := strictjson.Value[string](f["kind"])
	point, pointErr := strictjson.Value[string](f["point_id"])
	args := NativePointArgs{Kind: kind, PointID: point}
	if kindErr != nil || pointErr != nil || !args.Valid() {
		return nil, strictjson.ErrObject
	}
	return args, nil
}
