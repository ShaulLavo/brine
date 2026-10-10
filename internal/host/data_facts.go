package host

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type PersistentFacts interface {
	Collect(context.Context, policy.Desired) (target.Observation[[]target.PersistentDatabase], error)
}

// DataFacts observes existing approved reservations. It never allocates identities,
// data directories, receipts or schema definitions while inventory/planning runs.
type DataFacts struct {
	StateRoot     string
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
	if _, err := f.InspectPreparationRoots(ctx, desired); err != nil {
		return unknown, nil //nolint:nilerr // Unsafe policy-root inspection remains unknown evidence.
	}
	roots := map[data.PersistentRoot]data.RootEvidence{}
	mappings := map[data.PersistentRoot]data.MappingEvidence{}
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
		reserved, err := f.Store.ExistingDatabase(ctx, store.DataReservation{App: string(desired.Name), PolicyHash: desired.PolicyHash, Database: declaration, Destination: destination})
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			return unknown, nil
		}
		if err != nil {
			return unknown, err
		}
		reservations = append(reservations, reserved)
	}
	incarnation := reservations[0].Database.IncarnationID
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
		proofReceipt, proofErr := f.Store.ReadPreparationEvidence(ctx, b.DatabaseID)
		if proofErr != nil {
			return unknown, proofErr
		}
		if proofReceipt == nil || proofReceipt.RootProof == nil || proofReceipt.MappingProof == nil {
			return unknown, nil
		}
		root, err := data.InspectRoot(string(b.Root))
		proof := proofReceipt.RootProof
		if err != nil || root.Root != proof.Root || root.Device != proof.Device || root.Inode != proof.Inode || root.Filesystem != proof.Filesystem {
			return unknown, nil //nolint:nilerr // Root identity inspection failure cannot become admission.
		}
		root.POSIXLocks = proof.POSIXLocks
		root.DurableRename = proof.DurableRename
		if !root.Admits(b.Root, desired.MinimumFreeDiskBytes) || !proofReceipt.MappingProof.Admits(*desired.Runtime, root) {
			return unknown, nil
		}
		roots[b.Root] = root
		mappings[b.Root] = *proofReceipt.MappingProof
		schema := data.ObserveSchemaWithAllocation(ctx, b, definitions, receipt)
		permit, err := f.Store.ReadReplicaPermit(ctx, b.DatabaseID)
		if err != nil {
			return unknown, err
		}
		retention := target.Observation[data.RetentionEvidence]{Status: target.Unknown}
		for _, e := range desired.BackupRetention {
			if e.Admits(permit.Replica.Destination, time.Now().UTC()) {
				retention = target.Known(e)
			}
		}
		retained := make([]data.SchemaDefinition, 0)
		for _, definition := range definitions {
			if definition.Database == b.Name {
				retained = append(retained, definition)
			}
		}
		credentials := target.Observation[data.CredentialEvidence]{Status: target.Unknown}
		if f.StateRoot != "" {
			record, err := f.Store.CredentialReceipt(ctx, b.ReplicaBindingID, permit.Replica.CredentialVersion)
			if err == nil && record.EpochID == permit.Replica.EpochID && record.Destination == permit.Replica.Destination.Reference && record.CredentialRef == permit.Replica.Destination.CredentialRef {
				proof := data.CredentialEvidence{BindingID: record.BindingID, EpochID: record.EpochID, Destination: record.Destination, Reference: record.CredentialRef, Version: record.Version, PolicyHash: record.PolicyHash, ReceivedAt: record.ReceivedAt, ExpiresAt: record.ExpiresAt}
				files := backupcredentials.Files{Root: filepath.Join(f.StateRoot, "credentials")}
				secret, err := files.Read(record.CredentialRef, record.Version)
				if err == nil {
					secret.Clear()
					if proof.Admits(b.ReplicaBindingID, desired.PolicyHash, schema.ObservedAt, time.Minute) {
						credentials = target.Known(proof)
					}
				}
			}
		}
		usage := target.Observation[data.StorageUsage]{Status: target.Unknown}
		if measured, err := data.ObserveUsage(ctx, b); err == nil {
			usage = target.Known(measured)
		}
		facts = append(facts, target.PersistentDatabase{Credentials: credentials, Usage: usage, Definitions: retained, Database: b, Root: roots[b.Root], Mapping: target.Known(mappings[b.Root]), Retention: retention, Schema: schema, Fenced: permit.FenceState == "held"})
	}
	return target.Known(facts), nil
}

func PersistentExcludedRoots(home, stateDir string) []string {
	return []string{stateDir, filepath.Join(home, ".config/containers"), filepath.Join(home, ".config/systemd"), filepath.Join(home, ".local/share/containers")}
}

// InspectPreparationRoots reads policy-authorized ancestry and capacity without
// running filesystem probes, pulling images, or claiming mapping safety.
func (f DataFacts) InspectPreparationRoots(ctx context.Context, desired policy.Desired) ([]data.RootEvidence, error) {
	roots := []data.RootEvidence{}
	seen := map[data.PersistentRoot]bool{}
	for _, declaration := range desired.Databases {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !slices.Contains(desired.PersistentRoots, declaration.PersistentRoot) {
			return nil, data.ErrInvalid
		}
		for _, excluded := range f.ExcludedRoots {
			if data.OverlappingPaths(string(declaration.PersistentRoot), excluded) {
				return nil, data.ErrInvalid
			}
		}
		if seen[declaration.PersistentRoot] {
			continue
		}
		seen[declaration.PersistentRoot] = true
		root, err := data.InspectRoot(string(declaration.PersistentRoot))
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}
