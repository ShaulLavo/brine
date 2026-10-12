package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativePointInspection struct{ stateDir string }

func (r nativePointInspection) InspectNativePoint(ctx context.Context, args dispatch.NativePointArgs) (nativecapture.PointInspection, error) {
	if !args.Valid() {
		return nativecapture.PointInspection{}, result.New(result.InvalidUsage, nil)
	}
	state, err := store.OpenReadOnly(ctx, r.stateDir)
	if err != nil {
		return nativecapture.PointInspection{}, result.New(result.PolicyRefused, nil)
	}
	defer func() { _ = state.Close() }()
	if args.Kind == "verified" {
		point, err := state.ReadNativeVerifiedPoint(ctx, args.PointID)
		if err != nil {
			return nativecapture.PointInspection{}, nativePointInspectionError(err)
		}
		return nativecapture.PointInspection{Kind: args.Kind, PointID: point.PointID, PointSHA256: point.PointSHA256, OperationID: point.OperationID, BindingID: string(point.BindingID), EpochID: string(point.EpochID), CheckpointSHA256: point.CheckpointSHA256, SourceSHA256: point.SourceSHA256, ObservedAt: point.VerifiedAt, RetainUntil: point.RetainUntil, Admission: "unavailable"}, nil
	}
	point, err := state.ReadNativePreparedPoint(ctx, args.PointID)
	if err != nil {
		return nativecapture.PointInspection{}, nativePointInspectionError(err)
	}
	expires := point.ExpiresAt
	return nativecapture.PointInspection{Kind: args.Kind, PointID: point.PointID, PointSHA256: point.PointSHA256, OperationID: point.PreparationOperationID, BindingID: string(point.BindingID), EpochID: string(point.EpochID), CheckpointSHA256: point.CheckpointSHA256, SourceSHA256: point.SourceSHA256, ObservedAt: point.PreparedAt, ExpiresAt: &expires, RetainUntil: point.RetainUntil, Admission: "unavailable"}, nil
}

func nativePointInspectionError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return result.New(result.NativePointNotFound, nil)
	}
	return result.New(result.PolicyRefused, nil)
}
