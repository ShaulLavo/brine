package host

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type PersistentFacts interface {
	Collect(context.Context, policy.Desired) (target.Observation[[]target.PersistentDatabase], error)
}

// DataFacts is invoked under the host mutation lock. Root/mapping admission
// precedes reservations and private directory allocation; host effects never
// happen in store. Planning can allocate identity, never initialize SQLite.
type DataFacts struct {
	Store         *store.Store
	Runner        localexec.Runner
	ExcludedRoots []string
	ProbeRoot     func(context.Context, string) (data.RootEvidence, error)
	ProbeMapping  func(context.Context, localexec.Runner, data.RootEvidence, data.RuntimeIdentity) (data.MappingEvidence, error)
}

func (f DataFacts) Collect(ctx context.Context, desired policy.Desired) (target.Observation[[]target.PersistentDatabase], error) {
	unknown := target.Observation[[]target.PersistentDatabase]{Status: target.Unknown}
	if f.Store == nil || desired.Runtime == nil || len(desired.Databases) == 0 {
		return unknown, nil
	}
	probeRoot := f.ProbeRoot
	if probeRoot == nil {
		probeRoot = data.ProbeRoot
	}
	probeMapping := f.ProbeMapping
	if probeMapping == nil {
		probeMapping = inventory.ProbeDataMapping
	}
	roots := map[data.PersistentRoot]data.RootEvidence{}
	mappings := map[data.PersistentRoot]data.MappingEvidence{}
	for _, declaration := range desired.Databases {
		if !slices.Contains(desired.PersistentRoots, declaration.PersistentRoot) {
			return unknown, nil
		}
		for _, excluded := range f.ExcludedRoots {
			if data.OverlappingPaths(string(declaration.PersistentRoot), excluded) {
				return unknown, nil
			}
		}
		if _, ok := roots[declaration.PersistentRoot]; ok {
			continue
		}
		root, err := probeRoot(ctx, string(declaration.PersistentRoot))
		if err != nil || !root.Admits(declaration.PersistentRoot, desired.MinimumFreeDiskBytes) {
			return unknown, nil
		}
		mapping, err := probeMapping(ctx, f.Runner, root, *desired.Runtime)
		if err != nil || !mapping.Admits(*desired.Runtime, root) {
			return unknown, nil
		}
		roots[declaration.PersistentRoot] = root
		mappings[declaration.PersistentRoot] = mapping
	}
	reservations := make([]store.ReservedDatabase, 0, len(desired.Databases))
	for _, declaration := range desired.Databases {
		var destination data.Destination
		found := false
		for _, d := range desired.BackupDestinations {
			if d.Reference == declaration.BackupDestination {
				destination = d
				found = true
			}
		}
		if !found {
			return unknown, nil
		}
		reserved, err := f.Store.ReserveDatabase(ctx, store.DataReservation{App: string(desired.Name), PolicyHash: desired.PolicyHash, Database: declaration, Destination: destination})
		if err != nil {
			return unknown, err
		}
		history, err := f.Store.AllocationHistory(ctx, reserved.Database.DatabaseID)
		if err != nil {
			return unknown, err
		}
		if !history {
			if _, err = data.PrepareDirectory(reserved.Database); err != nil {
				return unknown, err
			}
		}
		receipt, err := f.Store.ReadAllocation(ctx, reserved.Database.DatabaseID)
		if err != nil {
			return unknown, err
		}
		if receipt == nil {
			if fresh, err := data.CaptureAllocation(reserved.Database); err == nil {
				// Existing history refuses replacing an absence proof. Continue with unknown
				// schema rather than repairing it or treating a missing old DB as empty.
				if err = f.Store.RecordAllocation(ctx, fresh); err != nil && !errors.Is(err, store.ErrConflict) {
					return unknown, err
				}
			}
		}
		reservations = append(reservations, reserved)
	}
	incarnation := reservations[0].Database.IncarnationID
	if err := f.Store.RegisterSchemaDefinitions(ctx, incarnation, desired.SchemaDefinitions); err != nil {
		return unknown, err
	}
	definitions, err := f.Store.ReadSchemaDefinitions(ctx, incarnation)
	if err != nil {
		return unknown, err
	}
	facts := make([]target.PersistentDatabase, 0, len(reservations))
	for _, reserved := range reservations {
		b := reserved.Database
		receipt, err := f.Store.ReadAllocation(ctx, b.DatabaseID)
		if err != nil {
			return unknown, err
		}
		schema := data.ObserveSchemaWithAllocation(ctx, b, definitions, receipt)
		permit, err := f.Store.ReadReplicaPermit(ctx, b.DatabaseID)
		if err != nil {
			return unknown, err
		}
		retention := target.Observation[data.RetentionEvidence]{Status: target.Unknown}
		for _, e := range desired.BackupRetention {
			if e.Admits(reserved.Replica.Destination, time.Now().UTC()) {
				retention = target.Known(e)
			}
		}
		retained := make([]data.SchemaDefinition, 0)
		for _, definition := range definitions {
			if definition.Database == b.Name {
				retained = append(retained, definition)
			}
		}
		facts = append(facts, target.PersistentDatabase{Definitions: retained, Database: b, Root: roots[b.Root], Mapping: target.Known(mappings[b.Root]), Retention: retention, Schema: schema, Fenced: permit.FenceState == "held"})
	}
	return target.Known(facts), nil
}

func PersistentExcludedRoots(home, stateDir string) []string {
	return []string{stateDir, filepath.Join(home, ".config/containers"), filepath.Join(home, ".config/systemd"), filepath.Join(home, ".local/share/containers")}
}
