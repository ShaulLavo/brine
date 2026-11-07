package host

import (
	"context"
	"errors"
	"os"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type controlPreview struct{ state string }

func (p controlPreview) DryRun(context.Context) (reconcile.Report, error) {
	return reconcile.Report{DryRun: true, ControlState: p.state, Outcomes: []reconcile.Outcome{}}, nil
}
func (controlPreview) Reconcile(context.Context) (reconcile.Report, error) {
	return reconcile.Report{}, result.New(result.DispatchOperationRefused, nil)
}

// openPreviewState never falls back to writable initialization or migration.
// Missing/older state is an explicit preview result, not an empty success claim.
func openPreviewState(ctx context.Context, dir string) (*Runtime, error) {
	s, err := openState(ctx, dir, statePreview, nil)
	if err != nil {
		state, failure := previewControlState(err)
		if failure != nil {
			return nil, failure
		}

		return &Runtime{Reconciler: controlPreview{state}, close: func() error { return nil }}, nil
	}
	return &Runtime{close: s.Close, previewStore: s.Store}, nil
}

// A preview cannot invent absent lock files. Report that limitation rather than
// weakening the launcher/host fence or writing a file in order to inspect it.
type readOnlyReconciler struct{ inner dispatch.ReconcileOperations }

func (r readOnlyReconciler) DryRun(ctx context.Context) (reconcile.Report, error) {
	report, err := r.inner.DryRun(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return controlPreview{"preview_unavailable"}.DryRun(ctx)
	}
	return report, err
}
func (readOnlyReconciler) Reconcile(context.Context) (reconcile.Report, error) {
	return reconcile.Report{}, result.New(result.DispatchOperationRefused, nil)
}

func previewControlState(err error) (string, error) {
	var schema *store.SchemaError
	switch {
	case errors.Is(err, admission.ErrUnavailable):
		return "", err
	case errors.Is(err, store.ErrPreviewUnavailable):
		return "preview_unavailable", nil
	case errors.Is(err, os.ErrNotExist):
		return "database_missing", nil
	case errors.As(err, &schema):
		if schema.Version >= 0 && schema.Version < store.SchemaVersion {
			return "schema_upgrade_required", nil
		}
		return "schema_unsupported", nil
	default:
		return "", err
	}
}
