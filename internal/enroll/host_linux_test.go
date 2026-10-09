package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type fakeAdmin struct {
	commands []localexec.Command
	handle   func(localexec.Command) (localexec.Result, error)
}

func (f *fakeAdmin) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	f.commands = append(f.commands, c)
	return f.handle(c)
}
func TestMaskBeforePackageTransaction(t *testing.T) {
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if c.Path == "apt-get" && c.Args[0] == "--simulate" {
			return localexec.Result{Stdout: "Inst fixture (1.0 Debian:13/stable [arm64])\n"}, nil
		}
		return localexec.Result{}, nil
	}}
	h := &host{exec: f, r: hostRecord{Packages: []Package{{"fixture", "1.0"}}}}
	if err := h.mask(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.installPackages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 3 || strings.Join(f.commands[0].Args, " ") != "mask caddy.service" {
		t.Fatal("package install preceded mask")
	}
	got := f.commands[2]
	if got.Path != "apt-get" || strings.Join(got.Args, " ") != "--yes --no-remove --no-install-recommends install fixture=1.0" || !got.Mutation {
		t.Fatalf("unbound install %+v", got)
	}
}
func TestChangedPackageTransactionRefused(t *testing.T) {
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		return localexec.Result{Stdout: "Inst unrelated (1.0 source)\n"}, nil
	}}
	h := &host{exec: f, r: hostRecord{Packages: []Package{{"fixture", "1.0"}}}}
	if err := h.installPackages(context.Background()); err == nil {
		t.Fatal("unexpected dependency accepted")
	}
	if len(f.commands) != 1 || f.commands[0].Mutation {
		t.Fatal("mutated before refusing")
	}
}
func TestRemoveOnlyInstalledPackages(t *testing.T) {
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		switch c.Path {
		case "dpkg-query":
			return localexec.Result{Stdout: "installed\t1.0"}, nil
		case "apt-get":
			if c.Args[0] == "--simulate" {
				return localexec.Result{Stdout: "Remv unrelated [1.0]\n"}, nil
			}
		}
		return localexec.Result{}, nil
	}}
	h := &host{exec: f, r: hostRecord{Packages: []Package{{"fixture", "1.0"}}}}
	if err := h.removePackages(context.Background()); err == nil {
		t.Fatal("preexisting package removal accepted")
	}
	for _, c := range f.commands {
		if c.Mutation {
			t.Fatal("mutated before refusal")
		}
	}
}
func TestRunnerOwnershipMarker(t *testing.T) {
	for _, tt := range []struct {
		line string
		want bool
	}{
		{"brine:x:1001:1001:brine-enrollment-fixture:/home/brine:/bin/sh", true},
		{"brine:x:1001:1001:unowned:/home/brine:/bin/sh", false},
		{"brine:x:1001:1001:brine-enrollment-fixture:/home/brine:/bin/bash", false},
		{"brine:x:1001:1001:brine-enrollment-fixture:/other:/bin/sh", false},
	} {
		f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) { return localexec.Result{Stdout: tt.line}, nil }}
		h := &host{exec: f, r: hostRecord{ID: "fixture"}}
		got, err := h.owned(context.Background())
		if err != nil || got != tt.want {
			t.Errorf("%s %v %v", tt.line, got, err)
		}
	}
}
func TestAdminExecutablePaths(t *testing.T) {
	f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) { return localexec.Result{}, nil }}
	h := &host{exec: f}
	for _, name := range []string{"useradd", "userdel", "runuser"} {
		if _, err := h.run(context.Background(), true, name, "fixture"); err != nil {
			t.Fatal(err)
		}
		c := f.commands[len(f.commands)-1]
		if c.Path != "/usr/sbin/"+name || c.Dir != "/tmp" || c.Timeout <= 0 {
			t.Fatalf("unsafe command %+v", c)
		}
	}
}
func TestUserAbsenceMustBeKnown(t *testing.T) {
	f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) {
		return localexec.Result{}, errors.New("probe failure")
	}}
	h := &host{exec: f, r: hostRecord{UID: -1}}
	if err := h.absence(context.Background()); err == nil {
		t.Fatal("unknown treated as absent")
	}
}
func TestExistingPackagesDoNotTouchMask(t *testing.T) {
	f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) {
		t.Fatal("touched existing packages")
		return localexec.Result{}, nil
	}}
	h := &host{exec: f}
	if err := h.mask(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.unmask(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.removePackages(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUserdelExit12AndUnknownOutcome(t *testing.T) {
	for _, kind := range []localexec.ErrorKind{localexec.Failed, localexec.UnknownOutcome} {
		for _, exists := range []bool{false, true} {
			f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
				if c.Path == "/usr/sbin/userdel" {
					return localexec.Result{ExitCode: 12}, &localexec.Error{Kind: kind, ExitCode: 12}
				}
				if exists {
					return localexec.Result{Stdout: "brine:x:1001:1001:brine-enrollment-fixture:/home/brine:/bin/sh"}, nil
				}
				return localexec.Result{ExitCode: 2}, &localexec.Error{Kind: localexec.Failed, ExitCode: 2}
			}}
			h := &host{exec: f, r: hostRecord{ID: "fixture"}}
			err := h.deleteAccount(context.Background())
			if (err == nil) == exists {
				t.Errorf("%s exists=%t err=%v", kind, exists, err)
			}
			deleted := 0
			for _, c := range f.commands {
				if c.Path == "/usr/sbin/userdel" {
					deleted++
				}
			}
			if deleted != 1 {
				t.Fatal("blind retry of deletion")
			}
		}
	}
}
func TestProductionStepOrderMatchesFailureMatrix(t *testing.T) {
	h := &host{}
	names := []string{}
	for _, s := range h.steps() {
		names = append(names, s.Name)
	}
	want := "ssh-layout mask packages user layout linger binary polkit caddy-tree caddy-import caddy-validate unmask caddy-enable caddy-start caddy-writable inventory-key operator-policy requester key ssh-policy bypass"
	if strings.Join(names, " ") != want {
		t.Fatal("update fault injection matrix for new production steps")
	}
}

func TestBoundedFileRefusesSymlinksAndDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := boundedRead(path)
	if err != nil || string(data) != "fixture" {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, dir} {
		if _, err := boundedRead(p); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
}
func TestStartupBypassHasNoCallerInput(t *testing.T) {
	h := &host{exec: &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if c.Path != "/usr/sbin/runuser" || len(c.Args) != 6 || c.Args[0] != "-u" || c.Args[1] != "brine" || c.Args[3] != "/bin/sh" || c.Args[4] != "-c" {
			t.Fatalf("unexpected bypass command %+v", c)
		}
		return localexec.Result{}, errors.New("permission probe failed")
	}}}
	if ok, err := h.bypass(context.Background()); ok || err == nil {
		t.Fatal("accepted failed bypass probe")
	}
}

func TestJournaledOriginalFileIsPendingNotDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	original := []byte("fixture { respond original }\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	h := host{r: hostRecord{Files: map[string]ownedFile{path: {Existed: true, Before: original, Hash: hash([]byte("replacement")), Mode: 0644, OriginalMode: 0600, OriginalUID: os.Getuid(), OriginalGID: os.Getgid()}}}}
	done, err := h.fileMatches(path, h.r.Files[path].Hash)
	if done || err != nil {
		t.Fatalf("original pending state: done=%v error=%v", done, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.fileMatches(path, h.r.Files[path].Hash); err == nil {
		t.Fatal("original metadata drift accepted")
	}
	if err := os.WriteFile(path, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.fileMatches(path, h.r.Files[path].Hash); err == nil {
		t.Fatal("unrelated bytes accepted")
	}
}

func TestPendingDirectoryCheckpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending")
	h := host{}
	d := ownedDir{UID: os.Getuid(), GID: os.Getgid(), Mode: 0755}
	if done, err := h.checkDirectory(path, d); done || err != nil {
		t.Fatalf("before mkdir: %v %v", done, err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if done, err := h.checkDirectory(path, d); done || err != nil {
		t.Fatalf("after mkdir/chown before inode checkpoint: %v %v", done, err)
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if done, err := h.checkDirectory(path, d); done || err != nil {
		t.Fatalf("after chmod before inode checkpoint: %v %v", done, err)
	}
	info, _ := os.Lstat(path)
	d.Inode = info.Sys().(*syscall.Stat_t).Ino
	d.Pending = true
	if done, err := h.checkDirectory(path, d); done || err != nil {
		t.Fatalf("after inode checkpoint: %v %v", done, err)
	}
	drift := d
	drift.Inode++
	if _, err := h.checkDirectory(path, drift); err == nil {
		t.Fatal("pending inode drift accepted")
	}
	d.Pending = false
	if done, err := h.checkDirectory(path, d); !done || err != nil {
		t.Fatalf("completed directory: %v %v", done, err)
	}
	d.Pending = true
	if err := os.WriteFile(filepath.Join(path, "unowned"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.checkDirectory(path, d); err == nil {
		t.Fatal("nonempty pending directory adopted")
	}
}

func TestCaddyCandidateFailureDoesNotTouchLiveRoot(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "Caddyfile")
	original := []byte("original root\n")
	if err := os.WriteFile(live, original, 0600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(dir, "candidate.caddy")
	if err := os.WriteFile(candidate, []byte("candidate import\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, unknown := range []bool{false, true} {
		f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
			if c.Args[len(c.Args)-1] != candidate {
				t.Fatal("validated live file instead of candidate")
			}
			if unknown {
				return localexec.Result{}, context.DeadlineExceeded
			}
			return localexec.Result{}, errors.New("missing certificate")
		}}
		h := host{exec: f}
		promoted := false
		err := h.validateAndPromote(context.Background(), candidate, func() error { promoted = true; return os.WriteFile(live, []byte("changed"), 0600) }, func() error { t.Fatal("validation failure attempted live recovery"); return nil })
		if err == nil || promoted {
			t.Fatalf("validation failure promoted=%v error=%v", promoted, err)
		}
		data, _ := os.ReadFile(live)
		info, _ := os.Stat(live)
		if string(data) != string(original) || info.Mode().Perm() != 0600 {
			t.Fatal("original bytes/metadata changed")
		}
	}
}
func TestCaddyCandidateValidatedBeforePromotion(t *testing.T) {
	validated := false
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) { validated = true; return localexec.Result{}, nil }}
	h := host{exec: f}
	if err := h.validateAndPromote(context.Background(), "candidate.caddy", func() error {
		if !validated {
			t.Fatal("promotion preceded validation")
		}
		return nil
	}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestBypassProbesAlternateProtectedKeyFile(t *testing.T) {
	observed := false
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if strings.Contains(strings.Join(c.Args, " "), "! touch /home/brine/.ssh/authorized_keys2") {
			observed = true
			return localexec.Result{}, errors.New("injected failed restriction")
		}
		return localexec.Result{}, nil
	}}
	h := host{exec: f}
	done, err := h.bypass(context.Background())
	if done || err == nil || !observed {
		t.Fatalf("alternate bypass was not checked: observed=%v error=%v", observed, err)
	}
}

func TestResumedTransactionCannotInstallAnOlderUnconfirmedIntent(t *testing.T) {
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		return localexec.Result{}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
	}}
	h := host{exec: f, r: hostRecord{Packages: []Package{{"podman", "5.4.2-1"}, {"fixture-dependency", "1.0"}}}}
	current := supported()
	current.PackageInstall = []Package{{"podman", "5.4.3-1"}, {"fixture-dependency", "1.0"}}
	if err := h.checkRecordedTransaction(context.Background(), current); err == nil {
		t.Fatal("old journal transaction overrode current confirmed versions")
	}
	current.PackageInstall = append([]Package{}, h.r.Packages...)
	if err := h.checkRecordedTransaction(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	current.PackageInstall = append(current.PackageInstall, Package{"new-dependency", "1.0"})
	if err := h.checkRecordedTransaction(context.Background(), current); err == nil {
		t.Fatal("new dependency accepted on resume")
	}
}

func TestInterruptedCaddyPromotionRestoresObservedOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	original := []byte("original bytes\n")
	want := []byte("intended bytes\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) { return localexec.Result{}, nil }}
	h := host{exec: f}
	restored := false
	err := h.validateAndPromote(context.Background(), "candidate", func() error {
		if err := os.WriteFile(path, want, 0600); err != nil {
			return err
		}
		if err := os.Chmod(path, 0644); err != nil {
			return err
		}
		return context.DeadlineExceeded
	}, func() error {
		data, err := os.ReadFile(path)
		if err != nil || hash(data) != hash(want) {
			return errors.New("unknown file contents; no blind rollback")
		}
		restored = true
		if err := os.WriteFile(path, original, 0600); err != nil {
			return err
		}
		return os.Chmod(path, 0600)
	})
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if err == nil || !restored || string(data) != string(original) || info.Mode().Perm() != 0600 {
		t.Fatalf("promotion recovery: restored=%v error=%v", restored, err)
	}
}

type keyOwnershipInfo struct {
	os.FileInfo
	UID uint32
}

func (i keyOwnershipInfo) Sys() any {
	st := *i.FileInfo.Sys().(*syscall.Stat_t)
	st.Uid = i.UID
	return &st
}
func TestKeyNodesRequireRootOwnershipAndNonWritableMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(path)
	if err := protectedKeyNode(keyOwnershipInfo{info, 0}, false); err != nil {
		t.Fatal(err)
	}
	if err := protectedKeyNode(keyOwnershipInfo{info, 1234}, false); err == nil {
		t.Fatal("runner-owned key accepted")
	}
	if err := os.Chmod(path, 0664); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Lstat(path)
	if err := protectedKeyNode(keyOwnershipInfo{info, 0}, false); err == nil {
		t.Fatal("group-writable root key accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("fixture", path); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Lstat(path)
	if err := protectedKeyNode(keyOwnershipInfo{info, 0}, false); err == nil {
		t.Fatal("symlink key accepted")
	}
}

func TestInterruptedLayoutRequiresRunnerStateDirectories(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if done, err := runnerStateComplete(root, os.Getuid(), os.Getgid()); done || err != nil {
		t.Fatalf("missing state considered complete: %v %v", done, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".local/state/brine"), 0700); err != nil {
		t.Fatal(err)
	}
	if done, err := runnerStateComplete(root, os.Getuid(), os.Getgid()); !done || err != nil {
		t.Fatalf("created state: %v %v", done, err)
	}
	if err := os.Remove(filepath.Join(dir, ".local/state/brine")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", filepath.Join(dir, ".local/state/brine")); err != nil {
		t.Fatal(err)
	}
	if _, err := runnerStateComplete(root, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("runner state symlink accepted")
	}
}
