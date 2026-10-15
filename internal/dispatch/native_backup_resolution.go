package dispatch

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"regexp"
)

type NativeBackupInspectArgs struct {
	OperationID string `json:"operation_id"`
}
type NativeBackupReviewArgs NativeBackupInspectArgs
type NativeBackupResolveArgs struct {
	ReviewID       string `json:"review_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type NativeBackupInspectionView struct {
	OperationID       string `json:"operation_id"`
	Kind              string `json:"kind"`
	BindingID         string `json:"binding_id"`
	EpochID           string `json:"epoch_id"`
	ObservationSHA256 string `json:"observation_sha256"`
	State             string `json:"state"`
	UnitsState        string `json:"units_state"`
}
type NativeBackupResolutionOperations interface {
	InspectBackup(context.Context, NativeBackupInspectArgs) (NativeBackupInspectionView, error)
	ReviewBackup(context.Context, NativeBackupReviewArgs) (nativecapture.BackupResolutionReview, error)
	ResolveBackup(context.Context, NativeBackupResolveArgs) (jobs.Accepted, error)
}

func decodeNativeBackupInspect(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "operation_id")
	if err != nil {
		return nil, err
	}
	op, err := strictjson.Value[string](fields["operation_id"])
	if err != nil || !jobs.ValidID(op) {
		return nil, strictjson.ErrObject
	}
	return NativeBackupInspectArgs{OperationID: op}, nil
}
func decodeNativeBackupReview(raw json.RawMessage) (any, error) {
	value, err := decodeNativeBackupInspect(raw)
	if err != nil {
		return nil, err
	}
	return NativeBackupReviewArgs(value.(NativeBackupInspectArgs)), nil
}
func decodeNativeBackupResolve(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "review_id", "idempotency_key")
	if err != nil {
		return nil, err
	}
	id, err := strictjson.Value[string](fields["review_id"])
	key, keyErr := strictjson.Value[string](fields["idempotency_key"])
	if err != nil || keyErr != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) || !jobs.ValidID(key) {
		return nil, strictjson.ErrObject
	}
	return NativeBackupResolveArgs{ReviewID: id, IdempotencyKey: key}, nil
}
func (s *Server) nativeBackupResolution(ctx context.Context, args any) (any, error) {
	if s.NativeBackupResolution == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	switch a := args.(type) {
	case NativeBackupInspectArgs:
		return s.NativeBackupResolution.InspectBackup(ctx, a)
	case NativeBackupReviewArgs:
		return s.NativeBackupResolution.ReviewBackup(ctx, a)
	case NativeBackupResolveArgs:
		return s.NativeBackupResolution.ResolveBackup(ctx, a)
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
}

func DecodeNativeBackupInspectionView(raw []byte) (NativeBackupInspectionView, error) {
	var v NativeBackupInspectionView
	fields := []string{"operation_id", "kind", "binding_id", "epoch_id", "observation_sha256", "state", "units_state"}
	if len(raw) == 0 || len(raw) > 4096 {
		return v, strictjson.ErrObject
	}
	if _, err := strictjson.Object(raw, fields...); err != nil {
		return v, err
	}
	if json.Unmarshal(raw, &v) != nil || !jobs.ValidID(v.OperationID) || v.Kind != "native_capture" && v.Kind != "native_prepare" && v.Kind != "native_backup_activate" || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(v.BindingID) || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(v.EpochID) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(v.ObservationSHA256) || v.State != "verified_remote" && v.State != "stop_not_issued" && (v.State != "stopped_unpublished" || v.Kind != "native_prepare" || v.UnitsState != "absent") {
		return v, strictjson.ErrObject
	}
	if v.Kind == "native_backup_activate" {
		if v.UnitsState != "not_issued" && v.UnitsState != "issued_absent" && v.UnitsState != "verified" {
			return v, strictjson.ErrObject
		}
	} else if v.State == "stop_not_issued" {
		if v.Kind != "native_prepare" || v.UnitsState != "running" && v.UnitsState != "stopped" {
			return v, strictjson.ErrObject
		}
	} else if v.State != "stopped_unpublished" && v.UnitsState != "" {
		return v, strictjson.ErrObject
	}
	return v, nil
}
