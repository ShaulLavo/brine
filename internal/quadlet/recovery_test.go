//go:build linux

package quadlet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestFabricatedMarkerIsNotOwnership(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	u := rendered(t, "new")
	forged := []byte(marker + "sha256:" + strings.Repeat("a", 64) + "\n[Container]\nImage=operator-owned\n")
	active := filepath.Join(home, ActiveDirectory, u.Name())
	if err = os.WriteFile(active, forged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Activate(context.Background(), u); err == nil {
		t.Fatal("fabricated comment granted overwrite authority")
	}
	checkFile(t, active, forged)
}
func TestActivationRetryCannotSkipBackupSync(t *testing.T) {
	home := t.TempDir()
	m, err := newTestManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old, next := rendered(t, "old"), rendered(t, "new")
	if _, err = m.Activate(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.syncDir = func(path string) error {
		if path == rollbackDirectory {
			calls++
			return errors.New("backup sync failure")
		}
		return m.syncDirectory(path)
	}
	for i := 0; i < 2; i++ {
		if _, err = m.Activate(context.Background(), next); err == nil {
			t.Fatal("retry skipped failed backup sync")
		}
		checkFile(t, filepath.Join(home, ActiveDirectory, old.Name()), old.Bytes())
	}
	if calls != 2 {
		t.Fatalf("backup durability was not retried: %d", calls)
	}
}
func TestRollbackRetryCannotSkipActiveSync(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "restore"}[restore], func(t *testing.T) {
			home := t.TempDir()
			m, err := newTestManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			old, next := rendered(t, "old"), rendered(t, "next")
			if restore {
				if _, err = m.Activate(context.Background(), old); err != nil {
					t.Fatal(err)
				}
			}
			r, err := m.Activate(context.Background(), next)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			m.syncDir = func(path string) error {
				if path == ActiveDirectory {
					calls++
					return errors.New("active sync failure")
				}
				return m.syncDirectory(path)
			}
			for i := 0; i < 2; i++ {
				if err = m.Rollback(context.Background(), r); !errors.Is(err, ErrPublicationUnknown) {
					t.Fatal("retry skipped failed publication sync", err)
				}
			}
			if calls != 2 {
				t.Fatalf("active durability was not retried: %d", calls)
			}
		})
	}
}

func TestCommittedHashesAreTheOnlyOverwriteAuthority(t *testing.T) {
	for _, kind := range []string{"unrecorded", "drift", "missing", "recorded without marker", "unsafe mode"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			m, err := NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			old, next := rendered(t, "old"), rendered(t, "next")
			content := old.Bytes()
			if kind == "recorded without marker" {
				content = []byte("[Container]\nImage=recorded-old-unit\n")
			}
			path := filepath.Join(home, ActiveDirectory, old.Name())
			if kind != "missing" {
				if err = os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			records := []target.Unit{{Name: old.Name(), Hash: digest(content)}}
			if kind == "unrecorded" {
				records = nil
			}
			if kind == "drift" {
				records[0].Hash = next.Hash()
			}
			if kind == "unsafe mode" {
				if err = os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			}
			r, err := m.Activate(context.Background(), next, records)
			if kind == "recorded without marker" {
				if err != nil {
					t.Fatal(err)
				}
				checkFile(t, path, next.Bytes())
				checkFile(t, filepath.Join(home, r.PreviousPath()), content)
				return
			}
			var ownership *OwnershipError
			if !errors.As(err, &ownership) {
				t.Fatal("ownership refusal was not typed", err)
			}
			want := map[string]OwnershipReason{"unrecorded": Unrecorded, "drift": HashMismatch, "missing": MissingRecorded, "unsafe mode": UnsafeFile}[kind]
			if ownership.Reason != want {
				t.Fatalf("wrong ownership reason: %s", ownership.Reason)
			}
			if kind != "missing" {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(content) {
					t.Fatal("refusal changed active bytes", err)
				}
			}
		})
	}
}

func TestEveryCreatedParentMustBeSyncedOnRetry(t *testing.T) {
	entries := []string{".config", ".config/containers", ActiveDirectory, ".local", ".local/share", ".local/share/brine", ".local/share/brine/quadlet", stagingDirectory, rollbackDirectory}
	for _, entry := range entries {
		t.Run(entry, func(t *testing.T) {
			home := t.TempDir()
			calls := 0
			failing := func(root *os.Root, path string) error {
				if _, entryErr := root.Lstat(entry); path == filepath.Dir(entry) && entryErr == nil {
					calls++
					return errors.New("injected parent sync failure")
				}
				d, err := root.Open(path)
				if err != nil {
					return err
				}
				defer d.Close()
				return d.Sync()
			}
			for i := 0; i < 2; i++ {
				m, err := newManager(home, accept(), failing)
				if err == nil {
					m.Close()
					t.Fatal("initialization bypassed a failed parent sync")
				}
			}
			if calls != 2 {
				t.Fatalf("parent sync was not retried: %d", calls)
			}
			m, err := NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if _, err = m.Activate(context.Background(), rendered(t, "first"), nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func cloneReceipt(t testing.TB, r Receipt) Receipt {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var copy Receipt
	if err = json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}
func committed(u Unit) []target.Unit { return []target.Unit{{Name: u.Name(), Hash: u.Hash()}} }

func TestActivationDurabilityStatesSurviveManagerRestart(t *testing.T) {
	cases := []struct {
		name  string
		state PublicationState
		file  bool
		path  func(Receipt) string
	}{
		{"ancestor", PathsPending, false, func(Receipt) string { return ".config/containers" }},
		{"backup file", PredecessorPending, true, func(r Receipt) string { return r.PreviousPath() }},
		{"backup directory", PredecessorPending, false, func(Receipt) string { return rollbackDirectory }},
		{"candidate parent", CandidatePending, false, func(Receipt) string { return stagingDirectory }},
		{"candidate file", CandidatePending, true, func(r Receipt) string { return filepath.Join(r.Activation.Directory, r.UnitName) }},
		{"candidate directory", CandidatePending, false, func(r Receipt) string { return r.Activation.Directory }},
		{"active file", ActiveSyncPending, true, func(r Receipt) string { return filepath.Join(ActiveDirectory, r.UnitName) }},
		{"active directory", ActiveSyncPending, false, func(Receipt) string { return ActiveDirectory }},
		{"source directory", SourceSyncPending, false, func(r Receipt) string { return r.Activation.Directory }},
		{"staging cleanup", StagingSyncPending, false, func(Receipt) string { return stagingDirectory }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			m, err := NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			old, next := rendered(t, "old"), rendered(t, "next")
			if _, err = m.Activate(context.Background(), old, nil); err != nil {
				t.Fatal(err)
			}
			r, err := m.PrepareActivation(next, committed(old))
			if err != nil {
				t.Fatal(err)
			}
			for r.Activation.State != tc.state {
				if err = m.AdvanceActivation(context.Background(), next, &r); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			inject := func(m *Manager) {
				m.syncDir = func(path string) error {
					if !tc.file && path == tc.path(r) {
						calls++
						return errors.New("injected directory sync failure")
					}
					return m.syncDirectory(path)
				}
				m.syncFile = func(f *os.File) error {
					if tc.file && (strings.HasSuffix(filepath.ToSlash(f.Name()), tc.path(r)) || strings.Contains(filepath.ToSlash(f.Name()), tc.path(r)+".")) {
						calls++
						return errors.New("injected file sync failure")
					}
					return f.Sync()
				}
			}
			inject(m)
			if err = m.AdvanceActivation(context.Background(), next, &r); err == nil {
				t.Fatal("accepted failed durability step")
			}
			if r.Activation.State != tc.state {
				t.Fatal("marked failed durability step complete", r.Activation.State)
			}
			r = cloneReceipt(t, r)
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			m, err = NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			inject(m)
			if err = m.AdvanceActivation(context.Background(), next, &r); err == nil {
				t.Fatal("restart skipped pending durability step")
			}
			if calls != 2 {
				t.Fatalf("pending sync attempts: %d", calls)
			}
			m.syncDir = m.syncDirectory
			m.syncFile = func(f *os.File) error { return f.Sync() }
			if err = m.ResumeActivation(context.Background(), next, &r); err != nil {
				t.Fatal(err)
			}
			if r.Activation.State != PublicationComplete {
				t.Fatal("activation did not reach durable completion")
			}
			checkFile(t, filepath.Join(home, ActiveDirectory, next.Name()), next.Bytes())
			checkFile(t, filepath.Join(home, r.PreviousPath()), old.Bytes())
		})
	}
}

func TestRollbackDurabilityStatesSurviveManagerRestart(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "restore"}[restore], func(t *testing.T) {
			home := t.TempDir()
			m, err := NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			old, next := rendered(t, "old"), rendered(t, "next")
			records := []target.Unit{}
			if restore {
				if _, err = m.Activate(context.Background(), old, nil); err != nil {
					t.Fatal(err)
				}
				records = committed(old)
			}
			r, err := m.Activate(context.Background(), next, records)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.PrepareRollback(&r); err != nil {
				t.Fatal(err)
			}
			for r.Rollback.State != ActiveSyncPending {
				if err = m.AdvanceRollback(context.Background(), &r); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			inject := func(m *Manager) {
				m.syncDir = func(path string) error {
					if path == ActiveDirectory {
						calls++
						return errors.New("injected sync failure")
					}
					return m.syncDirectory(path)
				}
			}
			inject(m)
			if err = m.AdvanceRollback(context.Background(), &r); !errors.Is(err, ErrPublicationUnknown) {
				t.Fatal(err)
			}
			r = cloneReceipt(t, r)
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			m, err = NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			inject(m)
			if err = m.Rollback(context.Background(), &r); !errors.Is(err, ErrPublicationUnknown) {
				t.Fatal("restart skipped pending rollback sync", err)
			}
			if r.Rollback.State != ActiveSyncPending || calls != 2 {
				t.Fatal("rollback durability was falsely confirmed")
			}
			m.syncDir = m.syncDirectory
			if err = m.Rollback(context.Background(), &r); err != nil {
				t.Fatal(err)
			}
			if r.Rollback.State != PublicationComplete {
				t.Fatal("rollback did not complete")
			}
			if restore {
				checkFile(t, filepath.Join(home, ActiveDirectory, old.Name()), old.Bytes())
			} else {
				if _, err = os.Stat(filepath.Join(home, ActiveDirectory, next.Name())); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("first installation remains", err)
				}
			}
		})
	}
}

func TestPendingRenameReconcilesWithoutOverwritingDrift(t *testing.T) {
	home := t.TempDir()
	m, err := NewManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old, next := rendered(t, "old"), rendered(t, "next")
	if _, err = m.Activate(context.Background(), old, nil); err != nil {
		t.Fatal(err)
	}
	r, err := m.PrepareActivation(next, committed(old))
	if err != nil {
		t.Fatal(err)
	}
	for r.Activation.State != RenamePending {
		if err = m.AdvanceActivation(context.Background(), next, &r); err != nil {
			t.Fatal(err)
		}
	}
	pending := cloneReceipt(t, r)
	if err = m.AdvanceActivation(context.Background(), next, &r); err != nil {
		t.Fatal(err)
	}
	if err = m.ResumeActivation(context.Background(), next, &pending); err != nil {
		t.Fatal("could not reconcile rename before checkpoint", err)
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, next.Name()), next.Bytes())
	operator := []byte("[Container]\nImage=operator-change\n")
	if err = os.WriteFile(filepath.Join(home, ActiveDirectory, next.Name()), operator, 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.Rollback(context.Background(), &pending); !errors.Is(err, ErrDrift) {
		t.Fatal("rollback did not reject unrecorded current bytes", err)
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, next.Name()), operator)
}

func TestEveryResumedStepRechecksParentOwnership(t *testing.T) {
	for _, stage := range []bool{false, true} {
		t.Run(map[bool]string{false: "predecessor parent", true: "candidate parent"}[stage], func(t *testing.T) {
			home := t.TempDir()
			m, err := NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			old, next := rendered(t, "old"), rendered(t, "next")
			if _, err = m.Activate(context.Background(), old, nil); err != nil {
				t.Fatal(err)
			}
			r, err := m.PrepareActivation(next, committed(old))
			if err != nil {
				t.Fatal(err)
			}
			state := PredecessorPending
			if stage {
				state = RenamePending
			}
			for r.Activation.State != state {
				if err = m.AdvanceActivation(context.Background(), next, &r); err != nil {
					t.Fatal(err)
				}
			}
			parent := rollbackDirectory
			if stage {
				parent = r.Activation.Directory
			}
			original := filepath.Join(home, parent)
			moved := original + "-operator"
			if err = os.Rename(original, moved); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(filepath.Base(moved), original); err != nil {
				t.Fatal(err)
			}
			if err = m.AdvanceActivation(context.Background(), next, &r); !errors.Is(err, ErrUnowned) {
				t.Fatal("resumed step accepted replaced parent", err)
			}
			checkFile(t, filepath.Join(home, ActiveDirectory, old.Name()), old.Bytes())
		})
	}
}

func TestRollbackNamespaceChangeBeforeCheckpointStillRequiresSync(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "restore"}[restore], func(t *testing.T) {
			home := t.TempDir()
			m, err := NewManager(home, accept())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			old, next := rendered(t, "old"), rendered(t, "next")
			records := []target.Unit{}
			if restore {
				if _, err = m.Activate(context.Background(), old, nil); err != nil {
					t.Fatal(err)
				}
				records = committed(old)
			}
			r, err := m.Activate(context.Background(), next, records)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.PrepareRollback(&r); err != nil {
				t.Fatal(err)
			}
			for r.Rollback.State != RenamePending {
				if err = m.AdvanceRollback(context.Background(), &r); err != nil {
					t.Fatal(err)
				}
			}
			persisted := cloneReceipt(t, r)
			if err = m.AdvanceRollback(context.Background(), &r); err != nil {
				t.Fatal(err)
			}
			calls := 0
			m.syncDir = func(path string) error {
				if path == ActiveDirectory {
					calls++
					return errors.New("injected sync failure")
				}
				return m.syncDirectory(path)
			}
			if err = m.Rollback(context.Background(), &persisted); !errors.Is(err, ErrPublicationUnknown) {
				t.Fatal("lost checkpoint skipped publication sync", err)
			}
			if calls != 1 || persisted.Rollback.State != ActiveSyncPending {
				t.Fatal("pending namespace change was not reconciled")
			}
			m.syncDir = m.syncDirectory
			if err = m.Rollback(context.Background(), &persisted); err != nil {
				t.Fatal(err)
			}
		})
	}
}
