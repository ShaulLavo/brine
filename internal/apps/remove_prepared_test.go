package apps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

type removalStore struct{ *fakeStore }
type removalLock struct{}

func (removalStore) AcquireHostLock(context.Context) (ops.Lock, error) {
	return removalLock{}, nil
}
func (removalLock) Release() error { return nil }

func TestRemovePreparedAppDoesNotSaveNoOp(t *testing.T) {
	s, db := configFixture(t)
	s.Store = removalStore{db}
	db.state.Releases = []plan.CurrentRelease{}
	db.state.DataIncarnations = []plan.DataIncarnation{{App: "hello", ID: strings.Repeat("a", 32)}}
	_, err := s.Lifecycle(context.Background(), "hello", plan.RemoveApp)
	if !errors.Is(err, plan.ErrPersistentData) {
		t.Fatalf("prepared app removal did not require archive: %v", err)
	}
	if len(db.saved) != 0 {
		t.Fatal("prepared app removal saved a misleading no-op plan")
	}
}
