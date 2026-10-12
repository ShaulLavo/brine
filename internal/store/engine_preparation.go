package store

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

// EnginePreparation keeps the short binding guard across protected material or
// unit preparation. Close it before CommitEngineRuntime, which takes that guard.
type EnginePreparation struct {
	root    string
	binding data.ReplicaBindingID
	guard   *data.EngineLock
}

func (p *EnginePreparation) Active(binding data.ReplicaBindingID) bool {
	return p != nil && p.binding == binding && p.guard.HoldsLaunch(p.root, binding)
}

func (p *EnginePreparation) StateRoot() string {
	if p == nil || !p.Active(p.binding) {
		return ""
	}
	return p.root
}

func (p *EnginePreparation) Close() error {
	if p == nil {
		return nil
	}
	return p.guard.Release()
}

func (s *Store) AcquireEnginePreparation(ctx context.Context, binding data.ReplicaBindingID, fence data.FenceID, operation string) (*EnginePreparation, error) {
	if s.readOnly || !data.ValidID(string(fence)) || operation == "" {
		return nil, ErrInvalid
	}
	reserved, err := s.ReadReservedEngine(ctx, binding)
	if err != nil {
		return nil, err
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, binding)
	if err != nil {
		return nil, err
	}
	if err = refusePendingNativeCaptureMutation(ctx, s.db, binding, operation); err != nil {
		return nil, errors.Join(err, guard.Release())
	}
	var held int
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM data_fences f JOIN data_active_incarnations a ON a.incarnation_id=f.incarnation_id WHERE f.database_id=? AND f.id=? AND f.operation_id=? AND f.state='held'", reserved.Database.DatabaseID, fence, operation).Scan(&held)
	if err != nil || held != 1 || !guard.HoldsLaunch(s.dir, binding) {
		return nil, errors.Join(ErrConflict, err, guard.Release())
	}
	return &EnginePreparation{root: s.dir, binding: binding, guard: guard}, nil
}
