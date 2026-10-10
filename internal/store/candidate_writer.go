package store

import (
	"context"
	"database/sql"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
)

// CandidateWriterSchema resolves an exact plan candidate before a first release
// exists. It returns no compatibility verdict: the host must freshly observe
// Bindings with WriterCompatibleWithAllocations. All held fences refuse.
func (s *Store) CandidateWriterSchema(ctx context.Context, id data.AppIncarnationID, desired policy.Desired) (_ WriterSchema, resultErr error) {
	if !data.ValidID(string(id)) {
		return WriterSchema{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WriterSchema{}, err
	}
	defer rollbackOnExit(tx, &resultErr)
	out, err := candidateWriterSchema(ctx, tx, s, id, desired)
	if err != nil {
		return WriterSchema{}, err
	}
	return out, tx.Commit()
}
func candidateWriterSchema(ctx context.Context, tx *sql.Tx, s *Store, id data.AppIncarnationID, desired policy.Desired) (WriterSchema, error) {
	if len(desired.Databases) == 0 || len(desired.Databases) > 16 || desired.Runtime == nil || desired.Runtime.Validate() != nil {
		return WriterSchema{}, ErrConflict
	}
	var app string
	if err := tx.QueryRowContext(ctx, "SELECT app FROM data_active_incarnations WHERE incarnation_id=?", id).Scan(&app); err != nil {
		return WriterSchema{}, err
	}
	if app != string(desired.Name) {
		return WriterSchema{}, ErrConflict
	}
	out := WriterSchema{Desired: desired, Bindings: make([]data.DatabaseBinding, 0, len(desired.Databases)), Allocations: map[data.DatabaseID]data.AllocationReceipt{}}
	definitions, err := readSchemaDefinitions(ctx, tx, id)
	if err != nil {
		return WriterSchema{}, err
	}
	out.Definitions = definitions
	registry := map[[2]string]string{}
	for _, d := range definitions {
		registry[[2]string{string(d.Database), d.Marker}] = d.CatalogSHA256
	}
	for _, d := range desired.SchemaDefinitions {
		if registry[[2]string{string(d.Database), d.Marker}] != d.CatalogSHA256 {
			return WriterSchema{}, ErrConflict
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_databases WHERE incarnation_id=?", id).Scan(&count); err != nil {
		return WriterSchema{}, err
	}
	if count != len(desired.Databases) {
		return WriterSchema{}, ErrConflict
	}
	seen := map[data.DatabaseName]bool{}
	for _, declaration := range desired.Databases {
		if declaration.Validate() != nil || seen[declaration.Name] {
			return WriterSchema{}, ErrConflict
		}
		seen[declaration.Name] = true
		var database data.DatabaseID
		if err = tx.QueryRowContext(ctx, "SELECT id FROM data_databases WHERE incarnation_id=? AND name=?", id, declaration.Name).Scan(&database); err != nil {
			return WriterSchema{}, ErrConflict
		}
		permit, err := readReplicaPermit(ctx, tx, database)
		if err != nil {
			return WriterSchema{}, err
		}
		if err = s.validateReplicaPermit(ctx, tx, permit); err != nil {
			return WriterSchema{}, err
		}
		binding := permit.Database
		if binding.IncarnationID != id || binding.Root != declaration.PersistentRoot || binding.MountPath != declaration.MountPath || binding.Filename != declaration.Filename || permit.Replica.Destination.Reference != declaration.BackupDestination || permit.FenceState == "held" {
			return WriterSchema{}, ErrConflict
		}
		complete := false
		for _, c := range desired.SchemaCompatibility {
			if c.Database == binding.Name {
				if complete || c.Startup != "preserve" || len(c.Accepts) == 0 {
					return WriterSchema{}, ErrConflict
				}
				complete = true
				for _, marker := range c.Accepts {
					if registry[[2]string{string(binding.Name), marker}] == "" {
						return WriterSchema{}, ErrConflict
					}
				}
			}
		}
		if !complete {
			return WriterSchema{}, ErrConflict
		}
		receipt, err := readAllocation(ctx, tx, database)
		if err != nil {
			return WriterSchema{}, err
		}
		if receipt != nil {
			if receipt.IncarnationID != id {
				return WriterSchema{}, ErrConflict
			}
			out.Allocations[database] = *receipt
		}
		out.Bindings = append(out.Bindings, binding)
	}
	return out, nil
}
