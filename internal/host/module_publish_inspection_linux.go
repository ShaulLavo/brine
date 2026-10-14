//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationPrivateInspectionWork struct {
	operations  modulePublications
	op          ops.Operation
	preparation *store.EnginePreparation
	lifetime    *data.EngineLock
	capability  *store.PublicationPrivateInspection
	facts       store.PublicationPrivateInspectionFacts
	original    *os.Root
	working     *os.Root
	view        *nativeReadView
}

func resolvePublicationPrivateSource(ctx context.Context, operations modulePublications, op ops.Operation) (_ datapublish.TaskReceipt, err error) {
	w := &publicationPrivateInspectionWork{operations: operations, op: op}
	defer func() { err = errors.Join(err, w.close()) }()
	if err = w.open(ctx); err != nil {
		return datapublish.TaskReceipt{}, err
	}
	observed, err := w.observe(ctx)
	if err != nil {
		return datapublish.TaskReceipt{}, err
	}
	return operations.service.Store.PreservePublicationOriginalSource(ctx, w.capability, observed)
}

func (w *publicationPrivateInspectionWork) open(ctx context.Context) error {
	s := w.operations.service.Store
	review, err := s.ReadPublicationReview(ctx, w.op.SecretRef)
	if err != nil {
		return err
	}
	point, _, _, err := s.ReadNativePreparedStopExpectation(ctx, review.Plan().Source().PointID())
	if err != nil {
		return err
	}
	w.preparation, err = s.AcquireEnginePreparation(ctx, point.BindingID, point.FenceID, point.FenceOwnerOpID)
	if err != nil {
		return err
	}
	w.lifetime, err = data.AcquireEngineLifetimeLock(ctx, w.operations.runtime.stateRoot, point.BindingID)
	if err != nil {
		return err
	}
	w.capability, err = s.BeginPublicationPrivateInspection(ctx, w.preparation, w.lifetime, w.op.SecretRef, w.op.RecoveryOf, w.op.ID)
	if err != nil {
		return err
	}
	w.facts, err = s.ReadPublicationPrivateInspectionFacts(ctx, w.capability)
	if err != nil {
		return err
	}
	w.original, err = openEngineSource(w.facts.Permit.Reserved.Database)
	if err != nil {
		return err
	}
	if len(w.facts.Effects) == 0 {
		return nil
	}
	w.working, err = engineOpenRoot(w.facts.Effects[0].RootPath)
	if err != nil {
		return err
	}
	identity, err := nativeRestoreDirectoryIdentity(w.working, ".")
	if err != nil || engineTransitionDirectory(identity) != w.facts.Effects[0].RootIdentity {
		return errEngineSource
	}
	return nil
}

func (w *publicationPrivateInspectionWork) observe(ctx context.Context) (store.PublicationSourcePreservationObservation, error) {
	zero := store.PublicationSourcePreservationObservation{}
	if w == nil || w.original == nil {
		return zero, datapublish.ErrRecord
	}
	if err := w.checkAbsent(ctx); err != nil {
		return zero, err
	}
	limits := engineCopyLimits{Bytes: w.facts.Permit.Runtime.CopyBytes, Files: w.facts.Permit.Runtime.CopyFiles, Depth: 16}
	before, err := sealOwnedEngineSource(ctx, w.original, limits)
	if err != nil || !engineSourceSealMatches(before, w.facts.Point.SourceSealSHA256) {
		return zero, errEngineSource
	}
	observed := store.PublicationSourcePreservationObservation{OriginalRoot: w.original, OriginalSealSHA256: before.hash, WorkingRoot: w.working, EngineAbsent: true, ApplicationAbsent: true, IngressClosed: true, PrivateReadersAbsent: true}
	if w.working != nil && !w.facts.Manifest.MatchesTree(ctx, w.working) {
		if err = w.inspectModule(ctx, &observed); err != nil {
			return zero, err
		}
	}
	if err = w.checkAbsent(ctx); err != nil {
		return zero, err
	}
	after, err := sealOwnedEngineSource(ctx, w.original, limits)
	if err != nil || after.hash != before.hash {
		return zero, errEngineSource
	}
	observed.ObservedAt = time.Now().UTC()
	return observed, nil
}

func (w *publicationPrivateInspectionWork) checkAbsent(ctx context.Context) error {
	s := w.operations.service.Store
	declaration, err := s.PublicationDeclarationID(ctx, w.op.SecretRef, w.op.Requester)
	if err != nil {
		return err
	}
	routes, withdrawn, previous, err := w.operations.runtime.preparedRouteStates(ctx, w.operations.service, w.facts.Point.PointID, w.op.App, declaration)
	if err != nil {
		return err
	}
	_, expected, _, err := s.ReadNativePreparedStopExpectation(ctx, w.facts.Point.PointID)
	if err != nil {
		return err
	}
	_, err = w.operations.runtime.inspectPreparedStop(ctx, w.op.App, w.facts.Permit, expected, routes, withdrawn, previous)
	return err
}

func (w *publicationPrivateInspectionWork) inspectModule(ctx context.Context, observed *store.PublicationSourcePreservationObservation) error {
	limits := engineCopyLimits{Bytes: w.facts.Permit.Runtime.CopyBytes, Files: w.facts.Permit.Runtime.CopyFiles, Depth: 16}
	sealed, err := sealEngineRoot(ctx, w.working, limits)
	if err != nil {
		return err
	}
	nonce, err := data.NewID()
	if err != nil {
		return err
	}
	inventory, err := nativeOwnedInventory(ctx, w.working, sealed, string(w.facts.Point.BindingID), string(w.facts.IncomingEpochID), nonce)
	if err != nil {
		return err
	}
	selection, err := nativecapture.Select(inventory, nativecapture.DefaultLimits())
	if err != nil {
		return err
	}
	w.view, err = materializeNativeStateSelection(ctx, w.working, w.operations.runtime.stateRoot, selection, nonce)
	if err != nil {
		return err
	}
	pin, err := readNativeReaderPin(ctx)
	if err != nil {
		return err
	}
	evidence, disposal, err := readNativeDiscoveryView(ctx, w.operations.runtime.session, pin, w.view, w.facts.Owner)
	if err != nil {
		return err
	}
	fresh, err := readNativeReaderPin(ctx)
	if err != nil || fresh != pin || disposal.nonce != nonce || disposal.viewHash != w.view.seal.hash || disposal.imageID != pin.ImageID || disposal.at.IsZero() {
		return errEngineSource
	}
	after, err := sealEngineRoot(ctx, w.working, limits)
	if err != nil || after.hash != sealed.hash {
		return errEngineSource
	}
	if err = w.view.close(); err != nil {
		return err
	}
	w.view = nil
	observed.WorkingEvidence, observed.ReaderNonce, observed.ReaderViewSHA256, observed.ReaderImageID, observed.ReaderDisposedAt = evidence, nonce, disposal.viewHash, pin.ImageID, disposal.at
	return nil
}

func (w *publicationPrivateInspectionWork) close() error {
	if w == nil {
		return nil
	}
	var err error
	if w.view != nil {
		err = errors.Join(err, w.view.close())
	}
	for _, root := range []*os.Root{w.working, w.original} {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}
	if w.lifetime != nil {
		err = errors.Join(err, w.lifetime.Release())
	}
	if w.preparation != nil {
		err = errors.Join(err, w.preparation.Close())
	}
	return err
}
