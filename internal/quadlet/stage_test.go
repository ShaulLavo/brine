//go:build linux

package quadlet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
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
func manager(t testing.TB, home string) *Manager {
	t.Helper()
	m, err := NewManager(home, accept())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallRollbackAndMultipleReleases(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	a, b, c := rendered(t, "a"), rendered(t, "b"), rendered(t, "c")
	for _, intent := range []struct {
		u   Unit
		old string
	}{{a, ""}, {b, a.Hash()}, {c, b.Hash()}} {
		for i := 0; i < 2; i++ {
			if err := m.Install(context.Background(), intent.u, intent.old); err != nil {
				t.Fatal(err)
			}
		}
	}
	active := filepath.Join(home, ActiveDirectory, c.Name())
	checkFile(t, active, c.Bytes())
	checkFile(t, filepath.Join(home, previousPath(c.Name())), b.Bytes())
	for i := 0; i < 2; i++ {
		if err := m.Rollback(context.Background(), c.Name(), c.Hash(), b.Hash()); err != nil {
			t.Fatal(err)
		}
	}
	checkFile(t, active, b.Bytes())
	checkFile(t, filepath.Join(home, previousPath(c.Name())), b.Bytes())
}
func TestFirstInstallRollback(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	u := rendered(t, "first")
	if err := m.Install(context.Background(), u, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := m.Rollback(context.Background(), u.Name(), u.Hash(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ActiveDirectory, u.Name())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("initial install remains", err)
	}
}

func TestRecordedOwnershipAndDrift(t *testing.T) {
	for _, kind := range []string{"fabricated marker", "mismatch", "missing", "markerless record", "unsafe permissions", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			m := manager(t, home)
			defer m.Close()
			old, next := rendered(t, "old"), rendered(t, "next")
			data := old.Bytes()
			hash := old.Hash()
			path := filepath.Join(home, ActiveDirectory, old.Name())
			if kind == "fabricated marker" {
				data = []byte(marker + "sha256:" + strings.Repeat("a", 64) + "\n[Container]\nImage=operator\n")
				hash = ""
			}
			if kind == "markerless record" {
				data = []byte("[Container]\nImage=recorded-old\n")
				hash = digest(data)
			}
			if kind != "missing" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "mismatch" {
				hash = next.Hash()
			}
			if kind == "unsafe permissions" {
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" || kind == "hardlink" {
				target := path + "-operator"
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(filepath.Base(target), path)
				} else {
					err = os.Link(target, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			err := m.Install(context.Background(), next, hash)
			if kind == "markerless record" {
				if err != nil {
					t.Fatal(err)
				}
				checkFile(t, filepath.Join(home, previousPath(next.Name())), data)
				return
			}
			var refusal *OwnershipError
			if !errors.As(err, &refusal) {
				t.Fatal("ownership refusal was not typed", err)
			}
			if kind != "missing" {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(data) {
					t.Fatal("refusal changed operator bytes", err)
				}
			}
		})
	}
}

func TestValidationIsolationCancellationAndTampering(t *testing.T) {
	for _, kind := range []string{"failure", "cancel", "candidate edit", "active edit", "parent link"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			m := manager(t, home)
			defer m.Close()
			old, next := rendered(t, "old"), rendered(t, "next")
			if err := m.Install(context.Background(), old, ""); err != nil {
				t.Fatal(err)
			}
			active := filepath.Join(home, ActiveDirectory, old.Name())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			operator := []byte("operator edit")
			m.validator = validatorFunc(func(_ context.Context, c Candidate) error {
				if strings.HasPrefix(c.Directory, filepath.Join(home, ActiveDirectory)) {
					t.Fatal("validation saw active units")
				}
				checkFile(t, filepath.Join(c.Directory, c.UnitName), next.Bytes())
				checkFile(t, active, old.Bytes())
				switch kind {
				case "failure":
					return errors.New("private validator output")
				case "cancel":
					cancel()
					return nil
				case "candidate edit":
					return os.WriteFile(filepath.Join(c.Directory, c.UnitName), operator, 0600)
				case "active edit":
					return os.WriteFile(active, operator, 0600)
				case "parent link":
					parent := filepath.Join(home, ActiveDirectory)
					if err := os.Rename(parent, parent+"-operator"); err != nil {
						return err
					}
					return os.Symlink(filepath.Base(parent)+"-operator", parent)
				}
				return nil
			})
			err := m.Install(ctx, next, old.Hash())
			if err == nil || strings.Contains(err.Error(), "private validator output") {
				t.Fatal("validation accepted unsafe output", err)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation type", err)
			}
			want := old.Bytes()
			if kind == "active edit" {
				want = operator
			}
			checkFile(t, active, want)
		})
	}
}

func TestReservedTempsAndUnrelatedFiles(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	u := rendered(t, "new")
	unrelated := filepath.Join(home, ActiveDirectory, "operator.container")
	otherTemp := filepath.Join(home, ActiveDirectory, temporaryName("other.container"))
	ownTemp := filepath.Join(home, ActiveDirectory, temporaryName(u.Name()))
	for _, path := range []string{unrelated, otherTemp, ownTemp} {
		if err := os.WriteFile(path, []byte("partial or operator"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Install(context.Background(), u, ""); err != nil {
		t.Fatal(err)
	}
	checkFile(t, unrelated, []byte("partial or operator"))
	checkFile(t, otherTemp, []byte("partial or operator"))
	if _, err := os.Stat(ownTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("own partial temp remains", err)
	}
}
func TestBackupCorruptionRefusesRollback(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	old, next := rendered(t, "old"), rendered(t, "next")
	if err := m.Install(context.Background(), old, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(context.Background(), next, old.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, previousPath(old.Name())), []byte("operator backup edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(context.Background(), next.Name(), next.Hash(), old.Hash()); !errors.Is(err, ErrDrift) {
		t.Fatal("accepted corrupt rollback source", err)
	}
	checkFile(t, filepath.Join(home, ActiveDirectory, next.Name()), next.Bytes())
}

func TestAtomicReadersAndHostLockSerialization(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	a, b := rendered(t, "a"), rendered(t, "b")
	if err := m.Install(context.Background(), a, ""); err != nil {
		t.Fatal(err)
	}
	var hostLock sync.Mutex
	old := a.Hash()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	reader := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				reader <- nil
				return
			default:
				data, err := os.ReadFile(filepath.Join(home, ActiveDirectory, a.Name()))
				if err != nil {
					reader <- err
					return
				}
				if string(data) != string(a.Bytes()) && string(data) != string(b.Bytes()) {
					reader <- errors.New("partial active file")
					return
				}
			}
		}
	}()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hostLock.Lock()
			defer hostLock.Unlock()
			u := a
			if i%2 == 0 {
				u = b
			}
			if err := m.Install(context.Background(), u, old); err != nil {
				t.Error(err)
			} else {
				old = u.Hash()
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	if err := <-reader; err != nil {
		t.Fatal(err)
	}
}

func TestStageLeavesActiveWriterArtifactUnchanged(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	previous, candidate := rendered(t, "previous"), rendered(t, "candidate")
	if err := m.Install(context.Background(), previous, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := m.Stage(context.Background(), candidate); err != nil {
			t.Fatal(err)
		}
		checkFile(t, filepath.Join(home, ActiveDirectory, previous.Name()), previous.Bytes())
	}
}
