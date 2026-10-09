//go:build linux

package quadlet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var interrupted = errors.New("injected interruption")

type operationFixture struct {
	home      string
	old, next Unit
	kind      string
}

func operation(t testing.TB, kind string) (operationFixture, *Manager) {
	t.Helper()
	f := operationFixture{home: t.TempDir(), old: rendered(t, "old"), next: rendered(t, "next"), kind: kind}
	m := manager(t, f.home)
	if kind != "create" && kind != "remove" {
		if err := m.Install(context.Background(), f.old, ""); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "restore" || kind == "remove" || kind == "already new" {
		hash := f.old.Hash()
		if kind == "remove" {
			hash = ""
		}
		if err := m.Install(context.Background(), f.next, hash); err != nil {
			t.Fatal(err)
		}
	}
	return f, m
}
func (f operationFixture) run(m *Manager) error {
	switch f.kind {
	case "create":
		return m.Install(context.Background(), f.next, "")
	case "restore":
		return m.Rollback(context.Background(), f.next.Name(), f.next.Hash(), f.old.Hash())
	case "remove":
		return m.Rollback(context.Background(), f.next.Name(), f.next.Hash(), "")
	default:
		return m.Install(context.Background(), f.next, f.old.Hash())
	}
}
func (f operationFixture) verify(t testing.TB) {
	t.Helper()
	active := filepath.Join(f.home, ActiveDirectory, f.next.Name())
	if f.kind == "remove" {
		if _, err := os.Stat(active); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("removed unit remains", err)
		}
		return
	}
	want := f.next.Bytes()
	if f.kind == "restore" {
		want = f.old.Bytes()
	}
	checkFile(t, active, want)
	if f.kind != "create" {
		checkFile(t, filepath.Join(f.home, previousPath(f.next.Name())), f.old.Bytes())
	}
}

func TestEveryFilesystemEffectConvergesAfterInterruption(t *testing.T) {
	for _, kind := range []string{"create", "update", "restore", "remove", "already new"} {
		t.Run(kind, func(t *testing.T) {
			baseline, m := operation(t, kind)
			var events []string
			m.after = func(event, path string) error { events = append(events, event); return nil }
			if err := baseline.run(m); err != nil {
				t.Fatal(err)
			}
			m.Close()
			for _, crash := range []bool{false, true} {
				mode := "error"
				if crash {
					mode = "crash"
				}
				for index, event := range events {
					t.Run(fmt.Sprintf("%s/%02d-%s", mode, index, event), func(t *testing.T) {
						f, m := operation(t, kind)
						calls := 0
						m.after = func(event, path string) error {
							calls++
							if calls != index+1 {
								return nil
							}
							if crash {
								if event == "file-created" {
									if err := os.WriteFile(filepath.Join(f.home, path), f.next.Bytes()[:17], 0600); err != nil {
										t.Fatal(err)
									}
								}
								panic(interrupted)
							}
							return interrupted
						}
						func() {
							defer func() {
								p := recover()
								if crash && p != interrupted {
									t.Fatal("crash hook did not fire", p)
								}
								if !crash && p != nil {
									panic(p)
								}
							}()
							err := f.run(m)
							if !crash && err == nil {
								t.Fatal("failed effect reported success")
							}
						}()
						m.Close()
						m = manager(t, f.home)
						defer m.Close()
						for i := 0; i < 2; i++ {
							if err := f.run(m); err != nil {
								t.Fatal("restart did not converge", err)
							}
						}
						f.verify(t)
					})
				}
			}
			t.Logf("%s covered %d filesystem effects with errors and crashes", kind, len(events))
		})
	}
}

func TestLostUnsyncedNamespaceConverges(t *testing.T) {
	for _, kind := range []string{"create", "update", "restore", "remove"} {
		t.Run(kind, func(t *testing.T) {
			f, m := operation(t, kind)
			active := filepath.Join(ActiveDirectory, f.next.Name())
			m.after = func(event, path string) error {
				if path == active && (event == "renamed" || event == "removed") {
					return interrupted
				}
				return nil
			}
			if err := f.run(m); err == nil {
				t.Fatal("namespace failure reported success")
			}
			m.Close()
			path := filepath.Join(f.home, active)
			if kind == "create" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				recovered := f.old.Bytes()
				if kind == "restore" || kind == "remove" {
					recovered = f.next.Bytes()
				}
				if err := os.WriteFile(path, recovered, 0600); err != nil {
					t.Fatal(err)
				}
			}
			m = manager(t, f.home)
			defer m.Close()
			if err := f.run(m); err != nil {
				t.Fatal("lost namespace did not converge", err)
			}
			f.verify(t)
		})
	}
}

func TestLateDriftNeverReportsSuccessOrCleansFiles(t *testing.T) {
	for _, kind := range []string{"update", "restore"} {
		t.Run(kind, func(t *testing.T) {
			f, m := operation(t, kind)
			active := filepath.Join(ActiveDirectory, f.next.Name())
			published := false
			m.after = func(event, path string) error {
				if event == "renamed" && path == active {
					published = true
				}
				if published && event == "directory-synced" && path == ActiveDirectory {
					return interrupted
				}
				return nil
			}
			if err := f.run(m); err == nil {
				t.Fatal("late interruption not injected")
			}
			m.Close()
			operator := []byte("operator edit")
			if err := os.WriteFile(filepath.Join(f.home, active), operator, 0600); err != nil {
				t.Fatal(err)
			}
			temp := filepath.Join(f.home, ActiveDirectory, temporaryName(f.next.Name()))
			if err := os.WriteFile(temp, []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			m = manager(t, f.home)
			defer m.Close()
			if err := f.run(m); !errors.Is(err, ErrDrift) {
				t.Fatal("late drift reported success", err)
			}
			checkFile(t, filepath.Join(f.home, active), operator)
			checkFile(t, temp, []byte("partial"))
		})
	}
}

func TestPermanentFsyncFailuresCannotBeSkipped(t *testing.T) {
	for _, kind := range []string{"update", "restore", "remove", "already new"} {
		for _, file := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/file=%t", kind, file), func(t *testing.T) {
				f, m := operation(t, kind)
				defer m.Close()
				calls := 0
				if file {
					m.syncFile = func(*os.File) error { calls++; return interrupted }
				} else {
					m.syncDir = func(path string) error {
						if path == ActiveDirectory {
							calls++
							return interrupted
						}
						return m.syncDirectory(path)
					}
				}
				for i := 0; i < 2; i++ {
					err := f.run(m)
					if file && kind == "remove" {
						if err != nil {
							t.Fatal(err)
						}
						break
					}
					if err == nil {
						t.Fatal("permanent fsync failure was bypassed")
					}
				}
				if !(file && kind == "remove") && calls != 2 {
					t.Fatalf("fsync retries: %d", calls)
				}
			})
		}
	}
}

func TestEveryCreatedParentIsSyncedOnRetry(t *testing.T) {
	entries := []string{".config", ".config/containers", ActiveDirectory, ".local", ".local/share", ".local/share/brine", ".local/share/brine/quadlet", stagingDirectory}
	for _, entry := range entries {
		t.Run(entry, func(t *testing.T) {
			home := t.TempDir()
			calls := 0
			sync := func(root *os.Root, path string) error {
				if _, err := root.Lstat(entry); path == filepath.Dir(entry) && err == nil {
					calls++
					return interrupted
				}
				f, err := root.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				return f.Sync()
			}
			for i := 0; i < 2; i++ {
				m, err := newManager(home, accept(), sync)
				if err == nil {
					m.Close()
					t.Fatal("failed parent sync was skipped")
				}
			}
			if calls != 2 {
				t.Fatal("parent sync not retried", calls)
			}
			m := manager(t, home)
			defer m.Close()
			if err := m.Install(context.Background(), rendered(t, "first"), ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEveryFsyncFailureConverges(t *testing.T) {
	for _, kind := range []string{"create", "update", "restore", "remove", "already new"} {
		t.Run(kind, func(t *testing.T) {
			f, m := operation(t, kind)
			count := 0
			m.syncFile = func(file *os.File) error { count++; return file.Sync() }
			m.syncDir = func(path string) error { count++; return m.syncDirectory(path) }
			if err := f.run(m); err != nil {
				t.Fatal(err)
			}
			m.Close()
			for index := 0; index < count; index++ {
				t.Run(fmt.Sprintf("fsync-%02d", index), func(t *testing.T) {
					f, m := operation(t, kind)
					calls := 0
					m.syncFile = func(file *os.File) error {
						calls++
						if calls == index+1 {
							return interrupted
						}
						return file.Sync()
					}
					m.syncDir = func(path string) error {
						calls++
						if calls == index+1 {
							return interrupted
						}
						return m.syncDirectory(path)
					}
					if err := f.run(m); err == nil {
						t.Fatal("failed fsync reported success")
					}
					m.Close()
					m = manager(t, f.home)
					defer m.Close()
					for i := 0; i < 2; i++ {
						if err := f.run(m); err != nil {
							t.Fatal("failed fsync prevented convergence", err)
						}
					}
					f.verify(t)
				})
			}
			t.Logf("%s covered %d fsync failures", kind, count)
		})
	}
}

func TestFinalSyncChecksActiveDrift(t *testing.T) {
	for _, kind := range []string{"update", "restore", "already new"} {
		t.Run(kind, func(t *testing.T) {
			f, m := operation(t, kind)
			defer m.Close()
			active := filepath.Join(ActiveDirectory, f.next.Name())
			published := kind == "already new"
			operator := []byte("operator edit during final sync")
			m.after = func(event, path string) error {
				if event == "renamed" && path == active {
					published = true
				}
				if published && event == "directory-synced" && path == ActiveDirectory {
					published = false
					return os.WriteFile(filepath.Join(f.home, active), operator, 0600)
				}
				return nil
			}
			if err := f.run(m); !errors.Is(err, ErrDrift) {
				t.Fatal("final active drift reported success", err)
			}
			checkFile(t, filepath.Join(f.home, active), operator)
		})
	}
}
