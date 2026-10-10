package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type WriterStartState string

const (
	WriterStartNone    WriterStartState = "none"
	WriterStartPending WriterStartState = "pending"
	WriterStartInvalid WriterStartState = "invalid"
)

type WriterStartIntent struct {
	OperationID   string
	PlanID        PlanID
	IncarnationID data.AppIncarnationID
	DesiredHash   string
	Desired       policy.Desired
}

// Pending is NOT permission. The read-only host adapter must also verify the
// operation's transient unit is active and freshly check the candidate schema.
// Invalid never permits fallback to a committed release.
type WriterStartResolution struct {
	State  WriterStartState
	Intent *WriterStartIntent
}

func (s *Store) BindWriterStart(ctx context.Context, operationID string, planID PlanID, incarnation data.AppIncarnationID, desiredHash string) error {
	if operationID == "" || len(operationID) > 256 || !data.ValidID(string(incarnation)) || !digestPattern.MatchString(desiredHash) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, d, err := loadPlan(ctx, tx, planID)
	if err != nil {
		return err
	}
	if p.Kind == plan.Conflict || p.DesiredHash != desiredHash {
		return ErrConflict
	}
	var operationPlan, state string
	if err = tx.QueryRowContext(ctx, "SELECT plan_id,state FROM operations WHERE id=?", operationID).Scan(&operationPlan, &state); err != nil {
		return err
	}
	if operationPlan != planID || state != "starting" {
		return ErrConflict
	}
	schema, err := candidateWriterSchema(ctx, tx, s, incarnation, d)
	if err != nil {
		return err
	}
	if !writerMountsMatch(p.DataMounts, schema.Bindings) {
		return ErrConflict
	}
	var oldPlan, oldIncarnation, oldHash string
	err = tx.QueryRowContext(ctx, "SELECT plan_id,incarnation_id,desired_hash FROM data_writer_starts WHERE operation_id=? AND cleared=0", operationID).Scan(&oldPlan, &oldIncarnation, &oldHash)
	if err == nil {
		if oldPlan != planID || oldIncarnation != string(incarnation) || oldHash != desiredHash {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_writer_starts(operation_id,plan_id,incarnation_id,desired_hash) VALUES(?,?,?,?)", operationID, planID, incarnation, desiredHash)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func writerMountsMatch(mounts []data.Mount, bindings []data.DatabaseBinding) bool {
	if len(mounts) != len(bindings) {
		return false
	}
	seen := map[data.DatabaseID]bool{}
	for _, mount := range mounts {
		if seen[mount.Database.DatabaseID] {
			return false
		}
		seen[mount.Database.DatabaseID] = true
		found := false
		for _, binding := range bindings {
			if mount.Database == binding && mount.BindingID == binding.ReplicaBindingID && mount.ContainerPath == binding.MountPath && mount.HostPath == filepath.Join(string(binding.Root), binding.RelativeDirectory) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (s *Store) ClearWriterStart(ctx context.Context, operationID string) error {
	if operationID == "" || len(operationID) > 256 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "UPDATE data_writer_starts SET cleared=1 WHERE operation_id=? AND cleared=0", operationID)
	return err
}
func (s *Store) ReadWriterStart(ctx context.Context, incarnation data.AppIncarnationID) (WriterStartResolution, error) {
	if !data.ValidID(string(incarnation)) {
		return WriterStartResolution{State: WriterStartInvalid}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WriterStartResolution{State: WriterStartInvalid}, err
	}
	defer tx.Rollback()
	intent := WriterStartIntent{IncarnationID: incarnation}
	var state, operationPlan string
	err = tx.QueryRowContext(ctx, "SELECT w.operation_id,w.plan_id,w.desired_hash,o.state,o.plan_id FROM data_writer_starts w JOIN operations o ON o.id=w.operation_id WHERE w.incarnation_id=? AND w.cleared=0", incarnation).Scan(&intent.OperationID, &intent.PlanID, &intent.DesiredHash, &state, &operationPlan)
	if errors.Is(err, sql.ErrNoRows) {
		return WriterStartResolution{State: WriterStartNone}, tx.Commit()
	}
	invalid := WriterStartResolution{State: WriterStartInvalid, Intent: &intent}
	if err != nil {
		return invalid, err
	}
	if operationPlan != intent.PlanID || (state != "starting" && state != "checking") {
		return invalid, tx.Commit()
	}
	p, d, err := loadPlan(ctx, tx, intent.PlanID)
	if err != nil {
		return invalid, err
	}
	if p.Kind == plan.Conflict || p.DesiredHash != intent.DesiredHash {
		return invalid, &IntegrityError{}
	}
	schema, err := candidateWriterSchema(ctx, tx, s, incarnation, d)
	if err != nil {
		return invalid, err
	}
	if !writerMountsMatch(p.DataMounts, schema.Bindings) {
		return invalid, &IntegrityError{}
	}
	intent.Desired = d
	return WriterStartResolution{State: WriterStartPending, Intent: &intent}, tx.Commit()
}
