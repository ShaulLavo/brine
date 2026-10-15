package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeMaterialPlanArgs struct {
	App                 string `json:"app"`
	DeliveryOperationID string `json:"delivery_operation_id"`
}
type NativeMaterialActivateArgs struct {
	PlanID string `json:"plan_id"`
}
type NativeMaterialActivationOperations interface {
	PlanMaterial(context.Context, NativeMaterialPlanArgs) (nativematerial.ActivationPlan, error)
	ActivateMaterial(context.Context, NativeMaterialActivateArgs) (nativematerial.ActivationReceipt, error)
}

func decodeNativeMaterialPlan(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "app", "delivery_operation_id")
	if err != nil {
		return nil, err
	}
	app, aerr := strictjson.Value[string](fields["app"])
	delivery, derr := strictjson.Value[string](fields["delivery_operation_id"])
	if aerr != nil || derr != nil || !ValidApp(app) || !jobs.ValidID(delivery) {
		return nil, strictjson.ErrObject
	}
	return NativeMaterialPlanArgs{App: app, DeliveryOperationID: delivery}, nil
}
func decodeNativeMaterialActivate(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "plan_id")
	if err != nil {
		return nil, err
	}
	id, err := strictjson.Value[string](fields["plan_id"])
	if err != nil || !data.ValidID(id) {
		return nil, strictjson.ErrObject
	}
	return NativeMaterialActivateArgs{PlanID: id}, nil
}
func (s *Server) materialActivation(ctx context.Context, args any) (any, error) {
	operations, ok := s.NativeMaterial.(NativeMaterialActivationOperations)
	if !ok {
		return nil, result.New(result.DependencyMissing, nil)
	}
	switch args := args.(type) {
	case NativeMaterialPlanArgs:
		return operations.PlanMaterial(ctx, args)
	case NativeMaterialActivateArgs:
		return operations.ActivateMaterial(ctx, args)
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
}

type EngineStopArgs struct {
	App string `json:"app"`
}

func decodeEngineStop(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "app")
	if err != nil {
		return nil, err
	}
	app, err := strictjson.Value[string](fields["app"])
	if err != nil || !ValidApp(app) {
		return nil, strictjson.ErrObject
	}
	return EngineStopArgs{App: app}, nil
}
func (s *Server) engineStop(ctx context.Context, args EngineStopArgs) (apps.ConfigPlan, error) {
	operations, ok := s.Config.(interface {
		EngineStop(context.Context, string) (apps.ConfigPlan, error)
	})
	if !ok {
		return apps.ConfigPlan{}, result.New(result.DependencyMissing, nil)
	}
	return operations.EngineStop(ctx, args.App)
}
