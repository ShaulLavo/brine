package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/replication"
)

type replicaServices struct {
	stopped bool
	stopErr error
	calls   []string
}

func (s *replicaServices) Start(_ context.Context, name string) error {
	s.calls = append(s.calls, "start "+name)
	return nil
}
func (s *replicaServices) Stop(_ context.Context, name string) error {
	s.calls = append(s.calls, "stop "+name)
	if s.stopErr == nil {
		s.stopped = true
	}
	return s.stopErr
}
func (s *replicaServices) ReplicaStopped(context.Context, string) (bool, error) {
	return s.stopped, nil
}

type replicaLock struct{ released bool }

func (l *replicaLock) Release() error { l.released = true; return nil }

type replicaLocks struct {
	lock  replicaLock
	err   error
	calls int
}

func (l *replicaLocks) Acquire(context.Context, string) (ReplicaLock, error) {
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	return &l.lock, nil
}
func TestReplicaQuiescenceRequiresStopAndIndependentLifetimeLock(t *testing.T) {
	s := &replicaServices{}
	locks := &replicaLocks{}
	r := ReplicaOrchestrator{Services: s, Locks: locks, Permits: fencedReplicaReader{}}
	b := ReplicaActivation{ReplicaPermitRequest: replication.ReplicaPermitRequest{BindingID: strings.Repeat("2", 32)}, LifetimeLock: "/state/replica-locks/" + strings.Repeat("2", 32) + ".lock"}
	if r.Quiesce(context.Background(), b) != nil {
		t.Fatal("quiescence failed")
	}
	if locks.calls != 1 || !locks.lock.released || len(s.calls) != 1 {
		t.Fatal("missing independent stop proof")
	}
	locks.err = errors.New("lock held")
	if r.Quiesce(context.Background(), b) == nil {
		t.Fatal("competing process treated as stopped")
	}
}
func TestReplicaUnknownStopNeverRetriesOrReleasesFence(t *testing.T) {
	s := &replicaServices{stopErr: errors.New("unknown stop")}
	locks := &replicaLocks{}
	r := ReplicaOrchestrator{Services: s, Locks: locks, Permits: fencedReplicaReader{}}
	b := ReplicaActivation{ReplicaPermitRequest: replication.ReplicaPermitRequest{BindingID: strings.Repeat("2", 32)}, LifetimeLock: "/state/replica-locks/" + strings.Repeat("2", 32) + ".lock"}
	if r.Quiesce(context.Background(), b) == nil {
		t.Fatal("unknown stop accepted")
	}
	if len(s.calls) != 1 || locks.calls != 0 {
		t.Fatal("unknown stop retried")
	}
	r.Permits = refusedPermitReader{}
	if r.Quiesce(context.Background(), b) == nil {
		t.Fatal("unfenced quiescence allowed")
	}
}

type refusedPermitReader struct{}

func (refusedPermitReader) ReadReplicaPermit(context.Context, string) (replication.PermitState, error) {
	return replication.PermitState{}, replication.ErrPermit
}
func (refusedPermitReader) ReadWriterPermits(context.Context, string) ([]replication.PermitState, error) {
	return nil, replication.ErrPermit
}
func TestReplicaActivationCannotBypassFence(t *testing.T) {
	s := &replicaServices{}
	r := ReplicaOrchestrator{Services: s, Permits: refusedPermitReader{}}
	b := ReplicaActivation{ReplicaPermitRequest: replication.ReplicaPermitRequest{BindingID: strings.Repeat("2", 32)}}
	if r.Activate(context.Background(), b) == nil {
		t.Fatal("activation bypassed permit")
	}
	if len(s.calls) != 0 {
		t.Fatal("called systemd while fenced")
	}
}

type fencedReplicaReader struct{}

func (fencedReplicaReader) ReadReplicaPermit(_ context.Context, id string) (replication.PermitState, error) {
	return replication.PermitState{Binding: replication.Binding{BindingID: id}, Fence: replication.FenceHeld, LifetimeLock: "/state/replica-locks/" + id + ".lock"}, nil
}
func (fencedReplicaReader) ReadWriterPermits(context.Context, string) ([]replication.PermitState, error) {
	return nil, replication.ErrPermit
}
