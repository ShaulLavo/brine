//go:build linux

package host

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
)

func TestRemovalEmptyOneAndTwoDatabasesUseSingleAggregate(t *testing.T) {
	for _, two := range []bool{false, true} {
		t.Run(map[bool]string{false: "one", true: "two"}[two], func(t *testing.T) { testRemovalEmpty(t, two) })
	}
}
func testRemovalEmpty(t *testing.T, two bool) {
	r := newEmptyRemovalHostRig(t, two)
	if len(r.plan.Removal.Points) != 0 {
		t.Fatal("invented preparation point")
	}
	m, err := r.service.Prepare(t.Context(), "hello", r.plan.ID)
	if err != nil || !m.Valid() {
		t.Fatal("prepare", err)
	}
	d, err := r.service.Retire(t.Context(), r.plan.ArchiveID)
	if err != nil || d.Stage != archive.Retired {
		t.Fatal("retire", d, err)
	}
	if r.receipt(t).Outcome != "retired" {
		t.Fatal("missing joined task completion")
	}
	r.noWriterEffects(t)
}

func TestRemovalEmptyPartialHoldFailureReleasesOwnResources(t *testing.T) {
	r := newEmptyRemovalHostRig(t, true)
	databases := r.plan.Candidate.Databases
	foreignPath := filepath.Join(r.prep.root, "replica-locks", string(databases[1].Reference().BindingID)+".lock")
	if err := ensurePrivateChild(r.prep.root, "replica-locks"); err != nil {
		t.Fatal(err)
	}
	foreign, err := replication.AcquireLifetimeLock(t.Context(), foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Release() }()
	if hold, err := r.host.HoldPrepared(t.Context(), r.plan); err == nil {
		_ = hold.Close()
		t.Fatal("foreign holder admitted")
	}
	firstPath := filepath.Join(r.prep.root, "replica-locks", string(databases[0].Reference().BindingID)+".lock")
	first, err := replication.AcquireLifetimeLock(t.Context(), firstPath)
	if err != nil {
		t.Fatal("first lifetime leaked", err)
	}
	_ = first.Release()
	lock, err := r.prep.state.AcquireHostLock(t.Context())
	if err != nil {
		t.Fatal("outer lock leaked", err)
	}
	_ = lock.Release()
	if unexpected, err := replication.AcquireLifetimeLock(t.Context(), foreignPath); err == nil {
		_ = unexpected.Release()
		t.Fatal("released foreign hold")
	}
	if data.VerifyUnarchivedTree(r.plan.Trees[0], r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
		t.Fatal("partial acquisition moved a source")
	}
}

type removalLegacyLockTrap struct {
	archiveHost
	calls int
}

func (h *removalLegacyLockTrap) AcquireLifetimeLocks(context.Context, archive.Plan) (func() error, error) {
	h.calls++
	return nil, archive.ErrRefused
}

func TestRemovalPreparedNeverEntersLegacyLockPath(t *testing.T) {
	r := newRemovalHostRigMode(t, true)
	trap := &removalLegacyLockTrap{archiveHost: r.host}
	r.service.Host = trap
	r.service.Lock = func(context.Context) (func() error, error) {
		t.Fatal("legacy service lock acquired")
		return nil, archive.ErrRefused
	}
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Retire(t.Context(), r.plan.ArchiveID); err != nil {
		t.Fatal(err)
	}
	if trap.calls != 0 {
		t.Fatal("duplicate lifetime acquisition", trap.calls)
	}
}
