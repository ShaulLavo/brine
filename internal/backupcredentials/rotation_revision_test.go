package backupcredentials

import "context"

// The rotation-only fixture has no cadence journal to reconcile.
func (f *rotationFake) ReconcileRevision(context.Context, Receipt) error { return nil }
