//go:build linux

package host

import (
	"context"
	"strings"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeMaterialStatusReader struct{ stateDir string }

func (nativeMaterialStatusReader) DeliverMaterial(context.Context, dispatch.NativeMaterialArgs) (nativematerial.Receipt, error) {
	return nativematerial.Receipt{}, result.New(result.DispatchOperationRefused, nil)
}
func (r nativeMaterialStatusReader) InspectMaterial(ctx context.Context, args dispatch.NativeMaterialInspectArgs) (nativematerial.Receipt, error) {
	raw, err := trustedRead(ctx, RequesterPath, 256)
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	requester := strings.TrimSpace(string(raw))
	identity, identityErr := datainit.ParseRequester(requester)
	if identityErr != nil || !identity.Valid() {
		return nativematerial.Receipt{}, result.New(result.DispatchOperationRefused, nil)
	}
	state, err := store.OpenReadOnly(ctx, r.stateDir)
	if err != nil {
		return nativematerial.Receipt{}, err
	}
	defer func() { _ = state.Close() }()
	return inspectNativeMaterial(ctx, state, requester, args)
}

func inspectNativeMaterial(ctx context.Context, state *store.Store, requester string, args dispatch.NativeMaterialInspectArgs) (nativematerial.Receipt, error) {
	if !args.Valid() {
		return nativematerial.Receipt{}, result.New(result.InvalidUsage, nil)
	}
	if args.OperationID != "" {
		return state.NativeMaterialReceipt(ctx, args.OperationID, requester)
	}
	input, err := state.NativeMaterialByKey(ctx, requester, args.IdempotencyKey)
	if err != nil || input.Receipt.App != args.App || input.Receipt.PreparationPlanID != args.PreparationPlanID {
		return nativematerial.Receipt{}, result.New(result.Conflict, nil)
	}
	return state.NativeMaterialReceipt(ctx, input.Receipt.OperationID, requester)
}
