package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func (s *Store) ArchiveCandidate(ctx context.Context, app string) (archive.Candidate, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return archive.Candidate{}, err
	}
	defer func() { _ = tx.Rollback() }()
	candidate, err := readArchiveCandidate(ctx, tx, app)
	if err != nil {
		return candidate, err
	}
	return candidate, tx.Commit()
}

func readArchiveCandidate(ctx context.Context, q dataQuerier, app string) (archive.Candidate, error) {
	return readArchiveCandidateOwned(ctx, q, app, "")
}
func readArchiveCandidateOwned(ctx context.Context, q dataQuerier, app, owner string) (archive.Candidate, error) {
	c := archive.Candidate{App: app, Databases: []archive.Database{}}
	if app == "" {
		return c, ErrInvalid
	}
	if err := q.QueryRowContext(ctx, "SELECT incarnation_id FROM data_active_incarnations WHERE app=?", app).Scan(&c.Incarnation); err != nil {
		return c, err
	}
	var pending int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM data_init_operations o JOIN data_fences f ON f.id=o.fence_id WHERE f.incarnation_id=? AND COALESCE((SELECT e.state FROM data_init_events e WHERE e.operation_id=o.id ORDER BY e.sequence DESC LIMIT 1),'unknown') NOT IN ('succeeded','not_initialized')`, c.Incarnation).Scan(&pending)
	if err != nil || pending != 0 {
		return c, ErrConflict
	}
	var invariants []data.RestoreInvariant
	err = q.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", app).Scan(&c.ReleaseID)
	if err == nil {
		release, err := readRelease(ctx, q, app, c.ReleaseID)
		if err != nil {
			return c, err
		}
		_, desired, err := loadPlan(ctx, q, release.PlanID)
		if err != nil {
			return c, err
		}
		invariants = desired.RestoreInvariants
	} else if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	definitions, err := readSchemaDefinitions(ctx, q, c.Incarnation)
	if err != nil {
		return c, err
	}
	rows, err := q.QueryContext(ctx, "SELECT id FROM data_databases WHERE incarnation_id=? ORDER BY id", c.Incarnation)
	if err != nil {
		return c, err
	}
	ids := []data.DatabaseID{}
	for rows.Next() {
		var id data.DatabaseID
		if err = rows.Scan(&id); err != nil {
			return c, errors.Join(err, rows.Close())
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return c, err
	}
	if len(ids) == 0 || len(ids) > 16 {
		return c, ErrConflict
	}
	for _, id := range ids {
		permit, err := readReplicaPermit(ctx, q, id)
		if err != nil || permit.Database.IncarnationID != c.Incarnation {
			return c, ErrConflict
		}
		if owner != "" && permit.FenceState != "held" {
			return c, ErrConflict
		}
		for _, fence := range permit.Fences {
			if fence.State == data.FenceHeld && (owner == "" || fence.OperationID != owner) {
				return c, ErrConflict
			}
		}
		var allocation data.AllocationReceipt
		var raw []byte
		if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_allocations WHERE database_id=?", id).Scan(&raw); err != nil {
			return c, err
		}
		if len(raw) > 4096 || json.Unmarshal(raw, &allocation) != nil || !allocation.Valid() || allocation.DatabaseID != id || allocation.IncarnationID != c.Incarnation {
			return c, &IntegrityError{}
		}
		database := archive.SQLiteDatabase{Binding: permit.Database, Allocation: &allocation, Definitions: []data.SQLiteSchemaDefinition{}, Invariants: []data.RestoreInvariant{}, Epochs: []data.ReplicaBinding{}, Retention: []data.RetentionEvidence{}}
		var written int
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM data_writer_history WHERE database_id=? UNION ALL SELECT 1 FROM data_writer_starts WHERE incarnation_id=?)`, id, c.Incarnation).Scan(&written); err != nil {
			return c, err
		}
		database.Written = written != 0
		for _, definition := range definitions {
			if definition.Database == permit.Database.Name {
				database.Definitions = append(database.Definitions, definition)
			}
		}
		for _, invariant := range invariants {
			if invariant.Database == permit.Database.Name {
				database.Invariants = append(database.Invariants, invariant)
			}
		}
		history, err := q.QueryContext(ctx, `SELECT canonical FROM data_replica_bindings WHERE id=? UNION ALL SELECT canonical FROM data_replica_commits WHERE binding_id=? ORDER BY canonical`, permit.Replica.BindingID, permit.Replica.BindingID)
		if err != nil {
			return c, err
		}
		epochs := map[data.ReplicaEpochID]data.ReplicaBinding{}
		for history.Next() {
			if err = history.Scan(&raw); err != nil {
				return c, errors.Join(err, history.Close())
			}
			var binding data.ReplicaBinding
			if len(raw) > 65536 || json.Unmarshal(raw, &binding) != nil || binding.BindingID != permit.Replica.BindingID || binding.DatabaseID != id {
				return c, errors.Join(&IntegrityError{}, history.Close())
			}
			previous, exists := epochs[binding.EpochID]
			if !exists || binding.CredentialVersion > previous.CredentialVersion {
				epochs[binding.EpochID] = binding
			}
		}
		if err = errors.Join(history.Err(), history.Close()); err != nil {
			return c, err
		}
		epochs[permit.Replica.EpochID] = permit.Replica
		for _, epoch := range epochs {
			database.Epochs = append(database.Epochs, epoch)
		}
		slices.SortFunc(database.Epochs, func(a, b data.ReplicaBinding) int { return strings.Compare(string(a.EpochID), string(b.EpochID)) })
		c.Databases = append(c.Databases, database)
	}
	return c, nil
}
