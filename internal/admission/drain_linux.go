//go:build linux

package admission

import (
	"context"
	"errors"
	"sync"
)

// Drain holds ordinary admission exclusively. It is not a quiescence proof.
// All root methods require external enrollment-record serialization and exact
// host/record binding. Close releases descriptors without reopening admission.
// Journal the kind section before effects; persist its terminal/reopening tail
// before Reopen. Only the caller may settle or abandon that journal.
type Drain struct {
	mu        sync.Mutex
	directory *Directory
	ordinary  *Lease
	stage     *Lease
	pending   Pending
	closed    bool
}

// CloseAdmission writes admitting before waiting for existing ordinary holders.
// Before this returns successfully the caller must issue no other effect. A
// bounded drain failure restores the previously open state. A failed restoration
// is returned, never treated as open. Existing closed state is never restored.
func (d *Directory) CloseAdmission(ctx context.Context, p Pending) (*Drain, error) {
	if p.Stage != Admitting || p.ExemptOpen || p.ExemptAttempt != "" {
		return nil, ErrInconsistent
	}
	before, err := d.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !before.Open {
		return nil, ErrClosed
	}
	if err = d.save(ctx, State{Version: 1, Pending: &p}); err != nil {
		return nil, err
	}
	ordinary, err := d.drain(ctx, "lock")
	if err != nil {
		restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultDrainTimeout)
		defer cancel()
		return nil, errors.Join(err, d.save(restore, before))
	}
	return &Drain{directory: d, ordinary: ordinary, pending: p}, nil
}

// ResumeClosed only resumes the exact selector already on disk. The caller
// first proves its matching enrollment/maintenance record under L1. No timeout
// or holder exit reopens this state. RevokeAndDrain is required before stage
// changes and is also performed by SetStage, EnableStage and Reopen.
func (d *Directory) ResumeClosed(ctx context.Context, kind Kind, id string) (*Drain, error) {
	s, err := d.Read(ctx)
	if err != nil {
		return nil, err
	}
	if s.Open || s.Pending.Kind != kind || s.Pending.ID != id {
		return nil, ErrInconsistent
	}
	ordinary, err := d.drain(ctx, "lock")
	if err != nil {
		return nil, err
	}
	return &Drain{directory: d, ordinary: ordinary, pending: *s.Pending}, nil
}

func (r *Drain) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.close()
}

func (r *Drain) close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	var err error
	if r.stage != nil {
		err = r.stage.Close()
	}
	return errors.Join(err, r.ordinary.Close())
}

func (r *Drain) current(ctx context.Context) (State, error) {
	if r.closed {
		return State{}, ErrClosed
	}
	s, err := r.directory.Read(ctx)
	if err != nil {
		return State{}, err
	}
	if s.Open || *s.Pending != r.pending {
		return State{}, ErrInconsistent
	}
	return s, nil
}

func (r *Drain) persist(ctx context.Context, p Pending) error {
	if err := r.directory.save(ctx, State{Version: 1, Pending: &p}); err != nil {
		return err
	}
	r.pending = p
	return nil
}

func (r *Drain) RevokeAndDrain(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revokeAndDrain(ctx)
}

func (r *Drain) revokeAndDrain(ctx context.Context) error {
	s, err := r.current(ctx)
	if err != nil {
		return err
	}
	p := *s.Pending
	p.ExemptOpen = false
	p.ExemptAttempt = ""
	if err = r.persist(ctx, p); err != nil {
		return err
	}
	if r.stage != nil {
		return nil
	}
	r.stage, err = r.directory.drain(ctx, "stage.lock")
	return err
}

// SetStage does not infer legal kind-specific transitions or journal completion.
// It only changes the accepted stage enum after revoking/draining all children.
func (r *Drain) SetStage(ctx context.Context, stage Stage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !validStage(stage) {
		return ErrInconsistent
	}
	if err := r.revokeAndDrain(ctx); err != nil {
		return err
	}
	p := r.pending
	p.Stage = stage
	return r.persist(ctx, p)
}

// EnableStage permits only the fixed update migration or restore open-check.
// It revokes and drains a prior attempt before creating a fresh generation.
// The stage lock is separate: ordinary admission remains held exclusively.
func (r *Drain) EnableStage(ctx context.Context, stage Stage) (Exemption, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !exemptStage(r.pending.Kind, stage) {
		return Exemption{}, ErrInconsistent
	}
	if err := r.revokeAndDrain(ctx); err != nil {
		return Exemption{}, err
	}
	attempt, err := freshID()
	if err != nil {
		return Exemption{}, err
	}
	p := r.pending
	p.Stage = stage
	p.ExemptOpen = true
	p.ExemptAttempt = attempt
	if err = r.persist(ctx, p); err != nil {
		return Exemption{}, err
	}
	err = r.stage.Close()
	r.stage = nil
	if err != nil {
		return Exemption{}, err
	}
	return Exemption{Kind: p.Kind, ID: p.ID, Stage: p.Stage, Attempt: p.ExemptAttempt}, nil
}

// Reopen publishes open state then releases ordinary admission, before the
// caller starts any runner handoff. It requires the terminal stage, but does
// not claim that the caller persisted or finished its reopening obligations.
func (r *Drain) Reopen(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending.Stage != Terminal {
		return ErrInconsistent
	}
	if err := r.revokeAndDrain(ctx); err != nil {
		return err
	}
	if err := r.directory.save(ctx, State{Version: 1, Open: true}); err != nil {
		return err
	}
	return r.close()
}
