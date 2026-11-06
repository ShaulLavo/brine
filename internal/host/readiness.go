package host

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func readinessReader(dir string, load func(context.Context) (dispatch.Inventory, error)) readiness.Reader {
	return readiness.Reader{Inventory: readinessInventory{dir: dir, load: load}, Runtime: inventory.Collector{FS: inventory.HostFS{}, Runner: localexec.ExecRunner{}}, Policy: readinessPolicy(), Store: readinessControl{dir: dir}, Network: readiness.NetworkProbes{}}
}

type readinessInventory struct {
	dir  string
	load func(context.Context) (dispatch.Inventory, error)
}

func (r readinessInventory) Collect(ctx context.Context) (target.Snapshot, error) {
	collector, err := r.load(ctx)
	if err != nil {
		return target.Snapshot{}, err
	}
	concrete, ok := collector.(*inventory.Collector)
	if !ok {
		return collector.Collect(ctx)
	}
	copy := *concrete
	copy.StateGeneration = nil
	copy.OperationState = nil
	copy.StateInventory = r.controlInventory
	return copy.CollectReadiness(ctx)
}
func (r readinessInventory) controlInventory(ctx context.Context) (_ target.ControlInventory, err error) {
	snapshot, err := store.OpenSnapshotReadOnly(ctx, r.dir)
	if err != nil {
		return target.ControlInventory{}, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	return snapshot.InventoryState(ctx)
}

type readinessControl struct{ dir string }

func (r readinessControl) Control(ctx context.Context) (_ readiness.Control, err error) {
	value := readiness.Control{State: "preview_unavailable", QuickCheck: readiness.Unknown[bool]("store_unavailable")}
	snapshot, err := store.OpenSnapshotReadOnly(ctx, r.dir)
	if err != nil {
		return readinessControlError(ctx, value, err)
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	value.State = "current"
	ok, err := snapshot.QuickCheck(ctx)
	if err != nil {
		return value, err
	}
	value.QuickCheck = readiness.Known(ok)
	return value, nil
}
func readinessControlError(ctx context.Context, value readiness.Control, err error) (readiness.Control, error) {
	if ctx.Err() != nil {
		return value, ctx.Err()
	}
	var schema *store.SchemaError
	switch {
	case errors.Is(err, os.ErrNotExist):
		value.State = "missing"
	case errors.As(err, &schema):
		value.State = "schema_unsupported"
		if schema.Version >= 0 && schema.Version < store.SchemaVersion {
			value.State = "schema_upgrade_required"
		}
	}
	return value, nil
}

func (r readinessControl) Backups(ctx context.Context) (_ []readiness.Backup, err error) {
	snapshot, err := store.OpenSnapshotReadOnly(ctx, r.dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	// These existing read ports include active preparations as well as releases.
	// An unrepresented preparation is unknown, never a known empty backup set.
	state, err := readinessState(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	if len(state.Releases) > readiness.MaxItems || len(state.DataIncarnations) > readiness.MaxItems {
		return nil, store.ErrInvalid
	}
	out := []readiness.Backup{}
	for _, release := range state.Releases {
		values, err := readinessReleaseBackups(ctx, r.dir, snapshot, release)
		if err != nil {
			return nil, err
		}
		out = append(out, values...)
	}
	for _, incarnation := range state.DataIncarnations {
		if !hasReadinessRelease(state.Releases, incarnation.App) {
			return nil, store.ErrNotFound
		}
	}
	if len(out) > readiness.MaxItems {
		return nil, store.ErrInvalid
	}
	return out, nil
}

func readinessState(ctx context.Context, snapshot *store.Store) (plan.BrineState, error) {
	control, err := snapshot.InventoryState(ctx)
	if err != nil {
		return plan.BrineState{}, err
	}
	identity := target.Identity{}
	if control.Target != nil {
		identity = *control.Target
	}
	return snapshot.LoadBrineState(ctx, identity, control.Generation)
}
func (r readinessControl) Routes(ctx context.Context) (_ []string, err error) {
	snapshot, err := store.OpenSnapshotReadOnly(ctx, r.dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	state, err := readinessState(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	domains := []string{}
	for _, release := range state.Releases {
		for _, domain := range release.Desired.Domains {
			domains = append(domains, string(domain))
		}
	}
	slices.Sort(domains)
	domains = slices.Compact(domains)
	if len(domains) > readiness.MaxItems {
		return nil, store.ErrInvalid
	}
	return domains, nil
}
