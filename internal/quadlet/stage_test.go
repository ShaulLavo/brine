//go:build linux

package quadlet

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type validatorFunc func(context.Context, Candidate) error

func (f validatorFunc) Validate(ctx context.Context, c Candidate) error { return f(ctx, c) }
func accept() Validator                                                 { return validatorFunc(func(context.Context, Candidate) error { return nil }) }
func rendered(t testing.TB, value string) Unit {
	t.Helper()
	d, _ := fixture(t)
	d.Environment = append(d.Environment, policy.Environment{Name: "VALUE", Value: value})
	u, err := Render(d, bind(t, d), manifest())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestActivateAndRollback(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	first, second := rendered(t, "first"), rendered(t, "second")
	r1, err := m.Activate(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := m.Activate(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(home, ActiveDirectory, first.Name())
	checkFile(t, active, second.Bytes())
	checkFile(t, filepath.Join(home, r2.PreviousPath()), first.Bytes())
	if err := m.Rollback(context.Background(), r2); err != nil {
		t.Fatal(err)
	}
	checkFile(t, active, first.Bytes())
	if err := m.Rollback(context.Background(), r2); err != nil {
		t.Fatal("repeat rollback", err)
	}
	if err := m.Rollback(context.Background(), r1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("first activation was not removed", err)
	}
	if err := m.Rollback(context.Background(), r1); err != nil {
		t.Fatal("repeat initial rollback", err)
	}
}
func checkFile(t testing.TB, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatal("unexpected file contents", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("artifact is not owner-only", err)
	}
}

func TestValidationIsOutsideActiveAndAtomic(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old, next := rendered(t, "old"), rendered(t, "next")
	_, err = m.Activate(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(home, ActiveDirectory, old.Name())
	calls := 0
	m.validator = validatorFunc(func(ctx context.Context, c Candidate) error {
		calls++
		if strings.HasPrefix(c.Directory, filepath.Join(home, ActiveDirectory)) {
			t.Fatal("staged in active directory")
		}
		if c.UnitName != next.Name() {
			t.Fatal("wrong candidate")
		}
		checkFile(t, active, old.Bytes())
		checkFile(t, filepath.Join(c.Directory, c.UnitName), next.Bytes())
		return errors.New("unsafe output that must not be echoed")
	})
	_, err = m.Activate(context.Background(), next)
	if err == nil || strings.Contains(err.Error(), "unsafe output") {
		t.Fatal("validation failure was not sanitized", err)
	}
	if calls != 1 {
		t.Fatal("validator not invoked")
	}
	checkFile(t, active, old.Bytes())
	entries, err := os.ReadDir(filepath.Join(home, stagingDirectory))
	if err != nil || len(entries) != 1 {
		t.Fatal("missing resumable candidate", err)
	}
	m.validator = accept()
	_, err = m.Activate(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, active, next.Bytes())
}
func TestRefuseUnownedAndSymlink(t *testing.T) {
	for _, kind := range []string{"unowned", "marker suffix", "symlink", "directory", "owned hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			m, err := newTestManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			u := rendered(t, "new")
			path := filepath.Join(home, ActiveDirectory, u.Name())
			other := filepath.Join(home, "other")
			original := []byte("[Container]\nImage=operator-owned\n")
			if err := os.WriteFile(other, original, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unowned":
				err = os.WriteFile(path, original, 0600)
			case "marker suffix":
				err = os.WriteFile(path, []byte(marker+"sha256:"+strings.Repeat("a", 64)+" garbage\n"), 0600)
			case "symlink":
				err = os.Symlink(other, path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "owned hardlink":
				err = os.WriteFile(other, u.Bytes(), 0600)
				if err == nil {
					err = os.Link(other, path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.Activate(context.Background(), u); err == nil {
				t.Fatal("overwrote unrelated file")
			}
			if kind != "owned hardlink" {
				checkFile(t, other, original)
			} else {
				checkFile(t, other, u.Bytes())
			}
		})
	}
	home := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	if m, err := newTestManager(home, accept()); err == nil {
		m.Close()
		t.Fatal("accepted symlinked directory")
	}
}
func TestCancellationTamperingAndRollbackDrift(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	u := rendered(t, "initial")
	r, err := m.Activate(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	next := rendered(t, "next")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = m.Activate(ctx, next); err == nil {
		t.Fatal("activated after cancellation")
	}
	m.validator = validatorFunc(func(ctx context.Context, c Candidate) error {
		return os.WriteFile(filepath.Join(c.Directory, c.UnitName), []byte("changed"), 0600)
	})
	if _, err = m.Activate(context.Background(), next); err == nil {
		t.Fatal("activated a modified candidate")
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, u.Name()), u.Bytes())
	m.validator = accept()
	_, err = m.Activate(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Rollback(context.Background(), r); err == nil {
		t.Fatal("rolled back over a newer release")
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, u.Name()), next.Bytes())
	if _, err = m.Activate(context.Background(), Unit{}); err == nil {
		t.Fatal("accepted zero unit")
	}
}
func TestConcurrentActivation(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	units := []Unit{rendered(t, "one"), rendered(t, "two")}
	var wg sync.WaitGroup
	for _, u := range units {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Activate(context.Background(), u); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(home, ActiveDirectory, units[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(units[0].Bytes()) && string(data) != string(units[1].Bytes()) {
		t.Fatal("partial activation")
	}
}

func TestRollbackRefusesChangedBackup(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old, next := rendered(t, "old"), rendered(t, "next")
	_, err = m.Activate(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Activate(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, r.PreviousPath()), old.Bytes()[:80], 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.Rollback(context.Background(), r); err == nil {
		t.Fatal("restored damaged rollback artifact")
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, next.Name()), next.Bytes())
}
func TestRefuseParentsReplacedAfterOpening(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	u := rendered(t, "initial")
	r, err := m.Activate(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	before := filepath.Join(home, ActiveDirectory)
	after := filepath.Join(home, "moved-active")
	if err = os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(after, before); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Activate(context.Background(), rendered(t, "next")); err == nil {
		t.Fatal("accepted replaced parent")
	}
	if err = m.Rollback(context.Background(), r); err == nil {
		t.Fatal("removed unit through replaced parent")
	}
	checkFile(t, filepath.Join(after, u.Name()), u.Bytes())
}
func TestContinuousReadersSeeCompleteUnits(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	one, two := rendered(t, "one"), rendered(t, "two")
	_, err = m.Activate(context.Background(), one)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				result <- nil
				return
			default:
			}
			b, err := os.ReadFile(filepath.Join(home, ActiveDirectory, one.Name()))
			if err != nil {
				result <- err
				return
			}
			if string(b) != string(one.Bytes()) && string(b) != string(two.Bytes()) {
				result <- errors.New("reader observed partial unit")
				return
			}
		}
	}()
	for i := 0; i < 25; i++ {
		u := one
		if i%2 == 0 {
			u = two
		}
		if _, err = m.Activate(context.Background(), u); err != nil {
			t.Error(err)
			break
		}
	}
	close(done)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}
func TestValidatorCannotChangeActiveOwnership(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	u := rendered(t, "initial")
	_, err = m.Activate(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(home, ActiveDirectory, u.Name())
	operator := []byte("[Container]\nImage=operator-owned\n")
	m.validator = validatorFunc(func(context.Context, Candidate) error { return os.WriteFile(active, operator, 0600) })
	if _, err = m.Activate(context.Background(), rendered(t, "next")); err == nil {
		t.Fatal("overwrote unowned replacement")
	}
	checkFile(t, active, operator)
	if m, err := newTestManager(t.TempDir(), nil); err == nil {
		m.Close()
		t.Fatal("accepted missing validator")
	}
}

func TestSyncFailureReturnsReceiptAndUnknownPublication(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old, next := rendered(t, "old"), rendered(t, "next")
	_, err = m.Activate(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	m.syncDir = func(path string) error {
		if path == ActiveDirectory {
			return errors.New("injected sync failure")
		}
		return m.syncDirectory(path)
	}
	receipt, err := m.Activate(context.Background(), next)
	if !errors.Is(err, ErrPublicationUnknown) || receipt.InstalledHash != next.Hash() {
		t.Fatal("lost unknown-outcome receipt", err)
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, next.Name()), next.Bytes())
	m.syncDir = m.syncDirectory
	if err = m.Rollback(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, old.Name()), old.Bytes())
}

func TestCancellationDuringValidation(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := newTestManager(home, validatorFunc(func(context.Context, Candidate) error { cancel(); return errors.New("raw validator error") }))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	u := rendered(t, "next")
	if _, err = m.Activate(ctx, u); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation did not retain its category", err)
	}
	if _, err = os.Stat(filepath.Join(home, ActiveDirectory, u.Name())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published after cancellation", err)
	}
}

// This ledger models caller-owned BrineState; it never reads ownership from disk.
type testManager struct {
	*Manager
	ledgerMu  sync.Mutex
	committed map[string]string
}

func newTestManager(home string, v Validator) (*testManager, error) {
	m, err := NewManager(home, v)
	if err != nil {
		return nil, err
	}
	return &testManager{Manager: m, committed: map[string]string{}}, nil
}
func (m *testManager) Activate(ctx context.Context, u Unit) (Receipt, error) {
	m.ledgerMu.Lock()
	defer m.ledgerMu.Unlock()
	records := []target.Unit{}
	for name, hash := range m.committed {
		records = append(records, target.Unit{Name: name, Hash: hash})
	}
	r, err := m.Manager.Activate(ctx, u, records)
	if err == nil {
		m.committed[u.Name()] = u.Hash()
	}
	return r, err
}
func (m *testManager) Rollback(ctx context.Context, r Receipt) error {
	m.ledgerMu.Lock()
	defer m.ledgerMu.Unlock()
	err := m.Manager.Rollback(ctx, &r)
	if err == nil {
		if r.PreviousHash == "" {
			delete(m.committed, r.UnitName)
		} else {
			m.committed[r.UnitName] = r.PreviousHash
		}
	}
	return err
}
