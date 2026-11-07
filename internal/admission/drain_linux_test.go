//go:build linux

package admission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func deadline(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func waitState(t *testing.T, d *Directory, predicate func(State) bool) {
	t.Helper()
	ctx := deadline(t, 10*time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s, err := d.Read(ctx)
		if err != nil && !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		// A concurrent root rename may invalidate the old read descriptor.
		// Only this test observer retries; production openers still refuse.
		if err == nil && predicate(s) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func closeAdmission(t *testing.T, d *Directory) *Drain {
	t.Helper()
	r, err := d.CloseAdmission(deadline(t, 10*time.Second), pendingFixture())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func TestOrdinaryLeaseBlocksDrainUntilClose(t *testing.T) {
	d, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	holder, err := d.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, holder)
	cancel() // A live store retains its lease even when its request is canceled.
	result := make(chan *Drain, 1)
	failures := make(chan error, 1)
	go func() {
		r, err := d.CloseAdmission(deadline(t, 10*time.Second), pendingFixture())
		result <- r
		failures <- err
	}()
	waitState(t, d, func(s State) bool { return !s.Open })
	if next, err := d.Acquire(context.Background()); !errors.Is(err, ErrClosed) {
		if next != nil {
			mustClose(t, next)
		}
		t.Fatal(err)
	}
	select {
	case <-result:
		t.Fatal("shared lease did not retain drain")
	default:
	}
	mustClose(t, holder)
	r := <-result
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	assertOpen(t, d, false)
	mustClose(t, r)
	assertOpen(t, d, false)
}

func TestNoEffectDrainCancellationRestoresOpen(t *testing.T) {
	d, _ := fixture(t)
	holder, err := d.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, holder)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, err := d.CloseAdmission(ctx, pendingFixture())
		if r != nil {
			err = errors.Join(err, r.Close())
		}
		done <- err
	}()
	waitState(t, d, func(s State) bool { return !s.Open })
	cancel()
	if err := <-done; !errors.Is(err, ErrDrainTimeout) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertOpen(t, d, true)
	lease, err := d.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, lease)
}

func TestTimeoutRestorationFailureStaysClosed(t *testing.T) {
	d, _ := fixture(t)
	holder, err := d.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, holder)
	rename := d.rename
	calls := 0
	injected := errors.New("restore write failed")
	d.rename = func(a int, b string, c int, e string) error {
		calls++
		if calls == 2 {
			return injected
		}
		return rename(a, b, c, e)
	}
	err = cancelAfterState(t, d, func(ctx context.Context) error { _, err := d.CloseAdmission(ctx, pendingFixture()); return err }, func(s State) bool { return !s.Open })
	if !errors.Is(err, ErrDrainTimeout) || !errors.Is(err, injected) {
		t.Fatal(err)
	}
	assertOpen(t, d, false)
}

func TestStageLeaseRevocationAndGeneration(t *testing.T) {
	d, _ := fixture(t)
	r := closeAdmission(t, d)
	e, err := r.EnableStage(context.Background(), Migrating)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := d.AcquireExempt(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, lease)
	// This child is paused before opening a store. The lease must still drain.
	done := make(chan error, 1)
	go func() { done <- r.SetStage(deadline(t, 10*time.Second), Captured) }()
	waitState(t, d, func(s State) bool { return !s.Pending.ExemptOpen })
	if stale, err := d.AcquireExempt(context.Background(), e); !errors.Is(err, ErrClosed) {
		if stale != nil {
			mustClose(t, stale)
		}
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("stage changed before paused opener drained")
	default:
	}
	mustClose(t, lease)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	fresh, err := r.EnableStage(context.Background(), Migrating)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Attempt == e.Attempt {
		t.Fatal("attempt reused")
	}
	if stale, err := d.AcquireExempt(context.Background(), e); !errors.Is(err, ErrClosed) {
		if stale != nil {
			mustClose(t, stale)
		}
		t.Fatal(err)
	}
	admitted, err := d.AcquireExempt(context.Background(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, admitted)
}

func TestStageTimeoutRetainsClosedAndRevoked(t *testing.T) {
	d, _ := fixture(t)
	r := closeAdmission(t, d)
	e, err := r.EnableStage(context.Background(), Migrating)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := d.AcquireExempt(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, lease)
	err = cancelAfterState(t, d, func(ctx context.Context) error { return r.SetStage(ctx, Terminal) }, func(s State) bool { return !s.Pending.ExemptOpen })
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatal(err)
	}
	s := assertOpen(t, d, false)
	if s.Pending.ExemptOpen || s.Pending.Stage != Migrating {
		t.Fatalf("%+v", s)
	}
	mustClose(t, lease)
	if err = r.SetStage(context.Background(), Terminal); err != nil {
		t.Fatal(err)
	}
	if err = r.Reopen(context.Background()); err != nil {
		t.Fatal(err)
	}
	admitted, err := d.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, admitted)
}

func TestResumeRevokesLostAttempt(t *testing.T) {
	d, _ := fixture(t)
	r := closeAdmission(t, d)
	e, err := r.EnableStage(context.Background(), Migrating)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := d.AcquireExempt(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, holder)
	mustClose(t, r) // Simulate parent loss after enabling a child, without reopening.
	resumed, err := d.ResumeClosed(context.Background(), e.Kind, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, resumed)
	if err = cancelAfterState(t, d, resumed.RevokeAndDrain, func(s State) bool { return !s.Pending.ExemptOpen }); !errors.Is(err, ErrDrainTimeout) {
		t.Fatal(err)
	}
	mustClose(t, holder)
	if err = resumed.SetStage(context.Background(), Terminal); err != nil {
		t.Fatal(err)
	}
	if err = resumed.Reopen(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertOpen(t, d, true)
}

func TestRefusalsHaveNoEffects(t *testing.T) {
	d, path := fixture(t)
	// #nosec G304 -- Exact test-owned admission fixture.
	before, err := os.ReadFile(filepath.Join(path, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = d.CloseAdmission(canceled, pendingFixture()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p := pendingFixture()
	p.ExemptOpen = true
	if _, err = d.CloseAdmission(context.Background(), p); err == nil {
		t.Fatal("caller exempt flag accepted")
	}
	// #nosec G304 -- Exact test-owned admission fixture.
	after, err := os.ReadFile(filepath.Join(path, "state.json"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("state changed: %s %v", after, err)
	}
	r := closeAdmission(t, d)
	if _, err = d.CloseAdmission(context.Background(), pendingFixture()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err = r.Reopen(context.Background()); !errors.Is(err, ErrInconsistent) {
		t.Fatal(err)
	}
	if _, err = r.EnableStage(context.Background(), Opening); !errors.Is(err, ErrInconsistent) {
		t.Fatal(err)
	}
	if _, err = d.ResumeClosed(context.Background(), Maintenance, p.ID); !errors.Is(err, ErrInconsistent) {
		t.Fatal(err)
	}
	if err = r.SetStage(context.Background(), "invented"); !errors.Is(err, ErrInconsistent) {
		t.Fatal(err)
	}
}

func cancelAfterState(t *testing.T, d *Directory, action func(context.Context) error, predicate func(State) bool) error {
	t.Helper()
	ctx, cancel := context.WithCancel(deadline(t, 10*time.Second))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- action(ctx) }()
	waitState(t, d, predicate)
	cancel()
	return <-done
}

func TestExemptionSelectorsNeverGrantAuthority(t *testing.T) {
	d, _ := fixture(t)
	zero := Exemption{}
	if lease, err := d.AcquireExempt(context.Background(), zero); !errors.Is(err, ErrClosed) {
		if lease != nil {
			mustClose(t, lease)
		}
		t.Fatal(err)
	}
	r := closeAdmission(t, d)
	e, err := r.EnableStage(context.Background(), Migrating)
	if err != nil {
		t.Fatal(err)
	}
	wrongKind, wrongID, wrongStage, wrongAttempt := e, e, e, e
	wrongKind.Kind = ControlRestore
	wrongID.ID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	wrongStage.Stage = Opening
	wrongAttempt.Attempt = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, selector := range []Exemption{zero, wrongKind, wrongID, wrongStage, wrongAttempt} {
		rejectExemption(t, d, selector)
	}
	lease, err := d.AcquireExempt(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, lease)
}

func rejectExemption(t *testing.T, d *Directory, e Exemption) {
	t.Helper()
	lease, err := d.AcquireExempt(context.Background(), e)
	if lease != nil {
		mustClose(t, lease)
	}
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("selector %+v: %v", e, err)
	}
}

func TestReopenPersistsBeforeReleasingOrdinaryLock(t *testing.T) {
	d, _ := fixture(t)
	r := closeAdmission(t, d)
	if err := r.SetStage(context.Background(), Terminal); err != nil {
		t.Fatal(err)
	}
	observed := false
	d.syncFile = func(f *os.File) error {
		if f == d.dir {
			observed = observeReopen(t, d) || observed
		}
		return f.Sync()
	}
	if err := r.Reopen(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("open state not synced before releasing ordinary lock")
	}
	lease, err := d.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, lease)
}

func observeReopen(t *testing.T, d *Directory) bool {
	t.Helper()
	s, err := d.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Open {
		return false
	}
	lease, err := d.Acquire(context.Background())
	if lease != nil {
		mustClose(t, lease)
	}
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("ordinary lock released before open sync: %v", err)
	}
	return true
}

func TestRestoreOpenCheckStage(t *testing.T) {
	d, _ := fixture(t)
	p := pendingFixture()
	p.Kind = ControlRestore
	r, err := d.CloseAdmission(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, r)
	e, err := r.EnableStage(context.Background(), Opening)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := d.AcquireExempt(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, lease)
	if err = r.RevokeAndDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	rejectExemption(t, d, e)
}
