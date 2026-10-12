package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"os"
	"time"
)

type NativeArchiveTreeMove struct {
	ArchiveID    data.ArchiveID               `json:"archive_id"`
	TransitionID string                       `json:"transition_id"`
	Sequence     uint64                       `json:"sequence"`
	Stage        string                       `json:"stage"`
	Tree         archive.NativeTransitionTree `json:"tree"`
	ObservedAt   time.Time                    `json:"observed_at"`
}

func readNativeArchiveTreeMove(ctx context.Context, q dataQuerier, id data.ArchiveID, transition string) (NativeArchiveTreeMove, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_archive_tree_events WHERE archive_id=? AND transition_id=? ORDER BY sequence DESC LIMIT 1", id, transition).Scan(&raw)
	if err != nil {
		return NativeArchiveTreeMove{}, err
	}
	var m NativeArchiveTreeMove
	if len(raw) > 65536 || json.Unmarshal(raw, &m) != nil || m.ArchiveID != id || m.TransitionID != transition || m.Sequence == 0 || m.Stage != "intent" && m.Stage != "moved" {
		return NativeArchiveTreeMove{}, &IntegrityError{}
	}
	canonical, _ := json.Marshal(m)
	if string(canonical) != string(raw) {
		return NativeArchiveTreeMove{}, &IntegrityError{}
	}
	return m, nil
}

func nativeArchiveMoveScope(ctx context.Context, tx *sql.Tx, id data.ArchiveID, transition string) (archive.Plan, archive.NativeTransitionTree, error) {
	p, err := loadArchivePlanByArchive(ctx, tx, id)
	if err != nil {
		return p, archive.NativeTransitionTree{}, err
	}
	disposition, exists, err := readArchiveDisposition(ctx, tx, id)
	if err != nil || !exists || disposition.Stage != archive.Renamed {
		return p, archive.NativeTransitionTree{}, ErrConflict
	}
	o, exists, err := readArchiveOperation(ctx, tx, p.ID)
	if err != nil || !exists || o.Stage != archive.Prepared {
		return p, archive.NativeTransitionTree{}, ErrConflict
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_archive_trees WHERE archive_id=? AND transition_id=?", id, transition).Scan(&raw); err != nil {
		return p, archive.NativeTransitionTree{}, err
	}
	var tree archive.NativeTransitionTree
	if json.Unmarshal(raw, &tree) != nil || tree.TransitionID != transition || tree.Seal.Validate() != nil {
		return p, tree, &IntegrityError{}
	}
	return p, tree, nil
}

func (s *Store) BeginNativeArchiveTreeMove(ctx context.Context, id data.ArchiveID, transition string, preparation *EnginePreparation, lifetime *data.EngineLock) (NativeArchiveTreeMove, bool, error) {
	var zero NativeArchiveTreeMove
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, false, err
	}
	defer func() { _ = tx.Rollback() }()
	p, tree, err := nativeArchiveMoveScope(ctx, tx, id, transition)
	if err != nil {
		return zero, false, err
	}
	if err = s.checkNativeArchiveMoveGuards(ctx, tx, p, tree, preparation, lifetime); err != nil {
		return zero, false, err
	}
	old, err := readNativeArchiveTreeMove(ctx, tx, id, transition)
	if err == nil {
		return old, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, false, err
	}
	var sequence uint64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM data_native_archive_tree_events WHERE archive_id=?", id).Scan(&sequence); err != nil {
		return zero, false, err
	}
	move := NativeArchiveTreeMove{ArchiveID: id, TransitionID: transition, Sequence: sequence, Stage: "intent", Tree: tree, ObservedAt: time.Now().UTC()}
	raw, _ := json.Marshal(move)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_archive_tree_events VALUES(?,?,?,?,?)", id, sequence, transition, move.Stage, raw); err != nil {
		return zero, false, err
	}
	return move, true, tx.Commit()
}

type NativeArchiveTreeObservation struct {
	Root        *os.Root
	Tree        data.EngineTreeSeal
	Lifetime    *data.EngineLock
	Preparation *EnginePreparation
}

func (NativeArchiveTreeObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func (s *Store) CompleteNativeArchiveTreeMove(ctx context.Context, old NativeArchiveTreeMove, observed NativeArchiveTreeObservation) error {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, tree, err := nativeArchiveMoveScope(ctx, tx, old.ArchiveID, old.TransitionID)
	if err != nil || tree != old.Tree || old.Stage != "intent" {
		return ErrConflict
	}
	if err = s.checkNativeArchiveMoveGuards(ctx, tx, p, tree, observed.Preparation, observed.Lifetime); err != nil {
		return err
	}
	if err = s.checkNativeArchiveMoveObservation(ctx, p, tree, observed); err != nil {
		return err
	}
	current, err := readNativeArchiveTreeMove(ctx, tx, old.ArchiveID, old.TransitionID)
	if err != nil || current != old {
		return ErrConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM data_native_archive_tree_events WHERE archive_id=?", old.ArchiveID).Scan(&old.Sequence); err != nil {
		return err
	}
	old.Stage = "moved"
	old.ObservedAt = time.Now().UTC()
	raw, _ := json.Marshal(old)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_archive_tree_events VALUES(?,?,?,?,?)", old.ArchiveID, old.Sequence, old.TransitionID, old.Stage, raw); err != nil {
		return err
	}
	return tx.Commit()
}

func checkNativeArchiveTreesMoved(ctx context.Context, q dataQuerier, p archive.Plan) error {
	for _, variant := range p.Candidate.Databases {
		native, ok := variant.Native()
		if !ok {
			continue
		}
		if err := checkNativeArchiveDatabaseTreesMoved(ctx, q, p.ArchiveID, native); err != nil {
			return err
		}
	}
	return nil
}
func checkNativeArchiveDatabaseTreesMoved(ctx context.Context, q dataQuerier, id data.ArchiveID, native archive.NativeDatabase) error {
	for _, tree := range native.TransitionTrees {
		m, err := readNativeArchiveTreeMove(ctx, q, id, tree.TransitionID)
		if err != nil || m.Stage != "moved" || m.Tree != tree {
			return ErrConflict
		}
	}
	return nil
}

func (s *Store) checkNativeArchiveMoveGuards(ctx context.Context, tx *sql.Tx, p archive.Plan, tree archive.NativeTransitionTree, preparation *EnginePreparation, lifetime *data.EngineLock) error {
	var binding data.ReplicaBindingID
	var original, owner string
	if err := tx.QueryRowContext(ctx, "SELECT binding_id,task_operation_id,original_fence_owner FROM data_native_archive_claims WHERE archive_id=?", p.ArchiveID).Scan(&binding, &original, &owner); err != nil {
		return err
	}
	if preparation == nil || preparation.StateRoot() != s.dir || !preparation.Active(binding) || !lifetime.HoldsLifetime(s.dir, binding) {
		return ErrConflict
	}
	var held int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_archive_fences a JOIN data_fences f ON f.id=a.fence_id JOIN data_archive_operations o ON o.id=a.operation_id WHERE o.plan_id=? AND f.operation_id=? AND f.state='held'", p.ID, owner).Scan(&held); err != nil || held != 1 {
		return ErrConflict
	}
	var state, requester, ref, kind string
	if err := tx.QueryRowContext(ctx, "SELECT state,requester,secret_ref,kind FROM operations WHERE id=?", original).Scan(&state, &requester, &ref, &kind); err != nil || requester != p.Requester || ref != p.ID || kind != "native_retire" {
		return ErrConflict
	}
	if state == "preflight" {
		return nil
	}
	if state != "recovery_required" {
		return ErrConflict
	}
	var resolve int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE recovery_of=? AND kind='native_retire_resolve' AND requester=? AND app=? AND secret_ref=? AND state='preflight'", original, p.Requester, p.Candidate.App, p.ID).Scan(&resolve); err != nil || resolve != 1 {
		return ErrConflict
	}
	return nil
}
