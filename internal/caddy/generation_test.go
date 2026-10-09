package caddy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

type fakeValidator struct {
	paths []string
	roots [][]byte
	err   error
}

func (f *fakeValidator) Validate(_ context.Context, path string) error {
	f.paths = append(f.paths, path)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f.roots = append(f.roots, b)
	return f.err
}

type fakeReloader struct {
	calls   int
	errors  []error
	observe func()
}

func (f *fakeReloader) Reload(context.Context) error {
	f.calls++
	if f.observe != nil {
		f.observe()
	}
	if f.calls <= len(f.errors) {
		return f.errors[f.calls-1]
	}
	return nil
}

func setup(t *testing.T) (*Manager, string, []byte, State, *fakeValidator, *fakeReloader) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "gen-0"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen-0", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	v, r := &fakeValidator{}, &fakeReloader{}
	m, err := NewManager(root, v, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	state, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	main := []byte("# operator config\n:8080 {\n respond ok\n}\nimport " + root + "/current/*.caddy\n")
	return m, root, main, state, v, r
}
func current(t *testing.T, root string) string {
	t.Helper()
	s, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func requireCurrent(t *testing.T, root, want string) {
	t.Helper()
	if got := current(t, root); got != want {
		t.Fatalf("current %s, want %s", got, want)
	}
}
func otherSite(t *testing.T) Site {
	t.Helper()
	a := fixtureApp(t)
	a.Name = "other"
	a.Domains = []spec.Domain{"other.example.com"}
	s, err := NewSite(a, fixturePolicy(t), 20002)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGenerationTransitions(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	original := append([]byte(nil), main...)
	first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err != nil || first.Outcome != Applied {
		t.Fatalf("%+v %v", first, err)
	}
	requireCurrent(t, root, "gen-1")
	if !reflect.DeepEqual(main, original) {
		t.Fatal("main config mutated")
	}
	wantRoot := strings.Replace(string(main), "/current/*.caddy", "/gen-1/*.caddy", 1)
	if string(v.roots[0]) != wantRoot {
		t.Fatal("candidate changed unrelated root bytes")
	}
	oldBytes, err := os.ReadFile(filepath.Join(root, "gen-1/hello.caddy"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Apply(context.Background(), main, first.Next, Put(otherSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(filepath.Join(root, "gen-2/hello.caddy"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preserved, oldBytes) {
		t.Fatal("unchanged file changed")
	}
	a := fixtureApp(t)
	a.Domains = []spec.Domain{"changed.example.com"}
	changed, err := NewSite(a, fixturePolicy(t), 20003)
	if err != nil {
		t.Fatal(err)
	}
	third, err := m.Apply(context.Background(), main, second.Next, Put(changed))
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(filepath.Join(root, "gen-3/hello.caddy"))
	if reflect.DeepEqual(updated, oldBytes) {
		t.Fatal("update ignored")
	}
	fourth, err := m.Apply(context.Background(), main, third.Next, Remove("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "gen-4/hello.caddy")); !os.IsNotExist(err) {
		t.Fatal("removed app retained")
	}
	if _, err := os.Stat(filepath.Join(root, "gen-3")); err != nil {
		t.Fatal("previous generation pruned")
	}
	for _, n := range []int{0, 1, 2} {
		if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("gen-%d", n))); !os.IsNotExist(err) {
			t.Fatal("old generation retained")
		}
	}
	requireCurrent(t, root, "gen-4")
	if fourth.Current != 4 || r.calls != 4 {
		t.Fatal("activation count mismatch")
	}
	fresh, err := NewManager(root, v, r)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.CheckDrift(fourth.Next); err != nil {
		t.Fatal("disk restart lost state", err)
	}
}

func TestValidateFailureDoesNotActivate(t *testing.T) {
	for _, reason := range []string{"invalid config", "duplicate site"} {
		t.Run(reason, func(t *testing.T) {
			m, root, main, state, v, r := setup(t)
			v.err = errors.New(reason)
			result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
			if err == nil || result.Outcome != Unchanged {
				t.Fatal("validation failure accepted")
			}
			requireCurrent(t, root, "gen-0")
			if r.calls != 0 {
				t.Fatal("reload after failed validation")
			}
		})
	}
}
func TestRootImportRefusals(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "relative", "inline", "zero change", "unsafe removal"} {
		t.Run(kind, func(t *testing.T) {
			m, root, main, state, v, r := setup(t)
			change := Put(fixtureSite(t))
			switch kind {
			case "missing":
				main = []byte(":8080 { respond ok }\n")
			case "duplicate":
				main = append(main, []byte("import "+root+"/current/*.caddy\n")...)
			case "relative":
				main = append(main, []byte("import extra/*.caddy\n")...)
			case "inline":
				main = append(main, []byte("import "+root+"/current/*.caddy # second\n")...)
			case "zero change":
				change = Change{}
			case "unsafe removal":
				change = Remove("../outside")
			}
			if _, err := m.Apply(context.Background(), main, state, change); err == nil {
				t.Fatal("unsafe root/change accepted")
			}
			requireCurrent(t, root, "gen-0")
			entries, _ := os.ReadDir(root)
			if len(entries) != 2 || len(v.paths) != 0 || r.calls != 0 {
				t.Fatal("refusal mutated disk")
			}
		})
	}
}

func TestReloadFailureRollsBack(t *testing.T) {
	m, root, main, state, _, r := setup(t)
	r.errors = []error{errors.New("reload refused")}
	var seen []string
	r.observe = func() { seen = append(seen, current(t, root)) }
	result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err == nil || result.Outcome != RolledBack || result.Current != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	if !reflect.DeepEqual(seen, []string{"gen-1", "gen-0"}) {
		t.Fatal("reload order", seen)
	}
	requireCurrent(t, root, "gen-0")
	if _, err := os.Stat(filepath.Join(root, "gen-1/hello.caddy")); err != nil {
		t.Fatal("rollback evidence removed")
	}
}
func TestRollbackReloadFailureRequiresRecovery(t *testing.T) {
	m, root, main, state, _, r := setup(t)
	r.errors = []error{errors.New("first"), errors.New("second")}
	result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err == nil || result.Outcome != RecoveryRequired {
		t.Fatalf("%+v %v", result, err)
	}
	requireCurrent(t, root, "gen-0")
}
func TestReloadUnknownLeavesDiskUntouched(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprint(rollback), func(t *testing.T) {
			m, root, main, state, _, r := setup(t)
			r.errors = []error{context.DeadlineExceeded}
			want := "gen-1"
			if rollback {
				r.errors = []error{errors.New("refused"), context.Canceled}
				want = "gen-0"
			}
			var before map[string]string
			r.observe = func() { before = diskImage(t, root) }
			result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
			var unknown *UnknownOutcomeError
			if !errors.As(err, &unknown) || result.Outcome != Unknown {
				t.Fatalf("%+v %v", result, err)
			}
			requireCurrent(t, root, want)
			if !reflect.DeepEqual(before, diskImage(t, root)) {
				t.Fatal("disk touched after unknown reload")
			}
			if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
				t.Fatal("blind retry accepted")
			}
		})
	}
}
func diskImage(t *testing.T, root string) map[string]string {
	t.Helper()
	image := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			image[rel] = target
			return err
		}
		if entry.IsDir() {
			image[rel] = "directory"
			return nil
		}
		b, err := os.ReadFile(path)
		image[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func TestCrashCheckpoints(t *testing.T) {
	for _, step := range []string{"generation-created", "file-written", "generation-synced", "candidate-written", "validated", "link-created", "current-renamed", "current-synced", "reloaded", "pruned"} {
		t.Run(step, func(t *testing.T) {
			m, root, main, state, _, r := setup(t)
			sentinel := errors.New("injected crash")
			m.checkpoint = func(got string) error {
				if got == step {
					return sentinel
				}
				return nil
			}
			result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
			if !errors.Is(err, sentinel) {
				t.Fatalf("step not reached %s %+v %v", step, result, err)
			}
			want := "gen-0"
			if step == "current-renamed" || step == "current-synced" || step == "reloaded" || step == "pruned" {
				want = "gen-1"
			}
			requireCurrent(t, root, want)
			if _, err := os.Stat(filepath.Join(root, "gen-0")); err != nil {
				t.Fatal("previous config lost")
			}
			if want == "gen-1" {
				fresh, err := NewManager(root, &fakeValidator{}, &fakeReloader{})
				if err != nil {
					t.Fatal(err)
				}
				defer fresh.Close()
				if err := fresh.CheckDrift(result.Next); err != nil {
					t.Fatal("activated partial generation", err)
				}
			} else if r.calls != 0 {
				t.Fatal("reload before atomic activation")
			}
		})
	}
}
func TestDriftRefusesMutation(t *testing.T) {
	for _, kind := range []string{"bytes", "added", "missing", "generation", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			m, root, main, state, v, r := setup(t)
			first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "gen-1/hello.caddy")
			switch kind {
			case "bytes":
				err = os.WriteFile(path, []byte("drift"), 0644)
			case "added":
				err = os.WriteFile(filepath.Join(root, "gen-1/other.caddy"), []byte("added"), 0644)
			case "missing":
				err = os.Remove(path)
			case "generation":
				first.Next.Generation++
			case "symlink":
				if err = os.Remove(path); err == nil {
					err = os.Symlink("../../outside", path)
				}
			case "directory":
				if err = os.Remove(path); err == nil {
					err = os.Mkdir(path, 0755)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := len(v.paths)
			if err := m.CheckDrift(first.Next); err == nil {
				t.Fatal("drift missed")
			}
			if _, err := m.Apply(context.Background(), main, first.Next, Remove("hello")); err == nil {
				t.Fatal("drift overwritten")
			}
			if len(v.paths) != before || r.calls != 1 {
				t.Fatal("drift validation/reload")
			}
		})
	}
}

func TestInactiveCrashRetrySkipsOrphan(t *testing.T) {
	m, root, main, state, _, r := setup(t)
	m.checkpoint = func(step string) error {
		if step == "generation-created" {
			return errors.New("crash")
		}
		return nil
	}
	if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
		t.Fatal("crash not injected")
	}
	m.checkpoint = nil
	result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	requireCurrent(t, root, "gen-2")
	if r.calls != 1 || result.Next.Generation != 2 {
		t.Fatal("orphan generation reused")
	}
	if _, err := os.Stat(filepath.Join(root, "gen-1")); !os.IsNotExist(err) {
		t.Fatal("inactive orphan not pruned")
	}
}

func TestFilesystemFailuresStayConfined(t *testing.T) {
	for _, kind := range []string{"generation symlink", "current escape", "candidate symlink", "activation collision", "generation overflow"} {
		t.Run(kind, func(t *testing.T) {
			m, root, main, state, v, r := setup(t)
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "generation symlink":
				err = os.Symlink(filepath.Dir(outside), filepath.Join(root, "gen-1"))
			case "current escape":
				if err = os.Remove(filepath.Join(root, "current")); err == nil {
					err = os.Symlink(filepath.Dir(outside), filepath.Join(root, "current"))
				}
			case "candidate symlink":
				err = os.Symlink(outside, filepath.Join(root, "candidate-gen-1.caddy"))
			case "activation collision":
				err = os.WriteFile(filepath.Join(root, ".current-gen-1"), []byte("collision"), 0644)
			case "generation overflow":
				err = os.Mkdir(filepath.Join(root, "gen-18446744073709551615"), 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
				t.Fatal("filesystem conflict accepted")
			}
			b, err := os.ReadFile(outside)
			if err != nil || string(b) != "untouched" {
				t.Fatal("outside file touched")
			}
			if kind != "current escape" {
				requireCurrent(t, root, "gen-0")
			}
			if r.calls != 0 {
				t.Fatal("reload after filesystem failure")
			}
			if kind != "activation collision" && len(v.paths) != 0 {
				t.Fatal("validation after preflight/write failure")
			}
		})
	}
}

func TestRollbackFilesystemCrash(t *testing.T) {
	for _, step := range []string{"rollback-link-created", "rollback-current-renamed", "rollback-current-synced"} {
		t.Run(step, func(t *testing.T) {
			m, root, main, state, _, r := setup(t)
			r.errors = []error{errors.New("refused")}
			m.checkpoint = func(got string) error {
				if got == step {
					return errors.New("rollback crash")
				}
				return nil
			}
			result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
			if err == nil || result.Outcome != RecoveryRequired || r.calls != 1 {
				t.Fatalf("%+v %v", result, err)
			}
			want := "gen-0"
			if step == "rollback-link-created" {
				want = "gen-1"
			}
			requireCurrent(t, root, want)
			if _, err := os.Stat(filepath.Join(root, "gen-0")); err != nil {
				t.Fatal("prior generation lost")
			}
		})
	}
}

func TestStaleStateAndValidationTimeout(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	v.err = fmt.Errorf("wrapped: %w", context.DeadlineExceeded)
	result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	var unknown *UnknownOutcomeError
	if err == nil || errors.As(err, &unknown) || result.Outcome != Unchanged {
		t.Fatal("validation timeout marked reload unknown")
	}
	requireCurrent(t, root, "gen-0")
	v.err = nil
	first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(context.Background(), main, state, Remove("hello")); err == nil {
		t.Fatal("stale state accepted")
	}
	if r.calls != 1 || first.Current != 2 {
		t.Fatal("stale mutation or orphan reuse")
	}
}

func TestAbsoluteCurrentAndCommentedImport(t *testing.T) {
	m, root, main, state, _, _ := setup(t)
	if err := os.Remove(filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gen-0"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	main = append([]byte("# import "+root+"/current/*.caddy\n"), main...)
	if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err != nil {
		t.Fatal(err)
	}
}

func TestPreCanceledDoesNotWrite(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	before := diskImage(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Apply(ctx, main, state, Put(fixtureSite(t))); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, diskImage(t, root)) || len(v.paths) != 0 || r.calls != 0 {
		t.Fatal("pre-canceled operation changed host")
	}
}

func TestInlineAndMultilineRelativeImportsRefused(t *testing.T) {
	for _, line := range []string{":8081 { import extra/*.caddy }\n", "import\nextra/*.caddy\n"} {
		m, root, main, state, _, r := setup(t)
		main = append(main, []byte(line)...)
		if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
			t.Fatal("relative import accepted")
		}
		requireCurrent(t, root, "gen-0")
		if r.calls != 0 {
			t.Fatal("unsafe candidate reloaded")
		}
	}
}

func TestPartialGenerationNeverSelected(t *testing.T) {
	m, root, main, state, _, _ := setup(t)
	first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("crash after first of two files")
	m.checkpoint = func(step string) error {
		if step == "file-written" {
			return sentinel
		}
		return nil
	}
	result, err := m.Apply(context.Background(), main, first.Next, Put(otherSite(t)))
	if !errors.Is(err, sentinel) || result.Outcome != Unchanged {
		t.Fatal(result, err)
	}
	requireCurrent(t, root, "gen-1")
	entries, err := os.ReadDir(filepath.Join(root, "gen-2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || len(result.Next.Files) != 2 {
		t.Fatal("partial write not exercised")
	}
	m.checkpoint = nil
	retry, err := m.Apply(context.Background(), main, first.Next, Put(otherSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	if retry.Next.Generation != 3 {
		t.Fatal("partial generation overwritten")
	}
	if err := m.CheckDrift(retry.Next); err != nil {
		t.Fatal(err)
	}
}

func TestPruneFailureStillReportsApplied(t *testing.T) {
	m, root, main, state, _, r := setup(t)
	first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "gen-0/unexpected"), 0755); err != nil {
		t.Fatal(err)
	}
	result, err := m.Apply(context.Background(), main, first.Next, Put(otherSite(t)))
	if err == nil || result.Outcome != Applied || r.calls != 2 {
		t.Fatal(result, err)
	}
	requireCurrent(t, root, "gen-2")
	if _, err := os.Stat(filepath.Join(root, "gen-1/hello.caddy")); err != nil {
		t.Fatal("previous generation lost")
	}
	if _, err := os.Stat(filepath.Join(root, "gen-0/unexpected")); err != nil {
		t.Fatal("unexpected directory deleted")
	}
	if err := m.CheckDrift(result.Next); err != nil {
		t.Fatal("cleanup failure poisoned active state", err)
	}
}
