//go:build linux

package host

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The fixed production admission path is real, root-owned and isolated by a
// user/mount namespace. No production path or owner override is needed.
func TestMain(m *testing.M) { os.Exit(runAdmissionFixture(m)) }

func runAdmissionFixture(m *testing.M) (code int) {
	if raw, err := os.ReadFile(admissionRoot + "/.test-namespace"); err == nil && string(raw) == "brine-host-tests-v1" {
		return m.Run()
	} // #nosec G304 -- Fixed namespace-only test marker; production gate remains active.
	switch os.Getenv("BRINE_HOST_GATE_FIXTURE") {
	case "ready":
		return m.Run()
	case "enter":
		return runAdmissionNamespace(m)
	}
	root, err := os.MkdirTemp("", "brine-gate-namespace-")
	if err != nil {
		return fixtureFailure(err)
	}
	defer func() {
		if err := removeAdmissionFixtureRoot(root); err != nil {
			code = fixtureFailure(err)
		}
	}()
	arguments, err := fixtureNamespaceArguments()
	if err != nil {
		return fixtureFailure(err)
	}
	command := exec.Command("/usr/bin/unshare", arguments...) // #nosec G204 G702 -- Fixed namespace launcher and this test binary.
	command.Env = append(os.Environ(), "BRINE_HOST_GATE_FIXTURE=enter", "BRINE_HOST_GATE_ROOT="+root, "BRINE_HOST_GATE_UID="+strconv.Itoa(os.Geteuid()), "BRINE_HOST_GATE_GID="+strconv.Itoa(os.Getegid()))
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr

	if err := command.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "host tests require an isolated user/mount namespace:", err)
		return 1
	}
	return 0
}

func runAdmissionNamespace(m *testing.M) int {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fixtureFailure(err)
	}
	root := os.Getenv("BRINE_HOST_GATE_ROOT")
	cwd, err := os.Getwd()
	if err != nil {
		return fixtureFailure(err)
	}
	if err = os.Chmod(root, 0755); err != nil { // #nosec G302 G703 -- Traversable parent-created namespace root.
		return fixtureFailure(err)
	} // #nosec G302 -- Traversable protected root for the original test UID.
	if err = os.Chown(root, 0, 0); err != nil { // #nosec G703 -- Parent-created scratch becomes root-owned only inside the subordinate-ID namespace.
		return fixtureFailure(err)
	}
	if err = bindFixtureRoots(root); err != nil {
		return fixtureFailure(err)
	}
	if err = bindFixtureHome(root); err != nil {
		return fixtureFailure(err)
	}
	if err = bindFixtureExecutable(root); err != nil {
		return fixtureFailure(err)
	}
	if err = unix.Chroot(root); err != nil {
		return fixtureFailure(err)
	}
	if err = os.Chdir(cwd); err != nil {
		return fixtureFailure(err)
	}
	if err = os.MkdirAll(admissionRoot, 0755); err != nil {
		return fixtureFailure(err)
	} // #nosec G301 -- Exact production metadata under the private root.
	if err := protectFixtureAdmissionDirectories(); err != nil {
		return fixtureFailure(err)
	}
	if err := os.WriteFile(admissionRoot+"/.test-namespace", []byte("brine-host-tests-v1"), 0644); err != nil {
		return fixtureFailure(err)
	} // #nosec G306 -- Namespace marker is intentionally readable by self-test subprocesses.
	if err := writeAdmissionFixture([]byte(`{"version":1,"open":true}`)); err != nil {
		return fixtureFailure(err)
	}
	if err := os.MkdirAll("/var/tmp", os.ModeSticky|0777); err != nil {
		return fixtureFailure(err)
	} // #nosec G301 -- Standard private namespace temp root.
	if err = os.Chmod("/var/tmp", os.ModeSticky|0777); err != nil {
		return fixtureFailure(err)
	} // #nosec G302 -- Standard temporary-directory metadata in the private namespace.
	if err = os.Chmod("/tmp", os.ModeSticky|0777); err != nil {
		return fixtureFailure(err)
	} // #nosec G302 -- Standard temporary-directory metadata in the private namespace.
	uid, err := strconv.Atoi(os.Getenv("BRINE_HOST_GATE_UID"))
	if err != nil {
		return fixtureFailure(err)
	}
	gid, err := strconv.Atoi(os.Getenv("BRINE_HOST_GATE_GID"))
	if err != nil {
		return fixtureFailure(err)
	}
	defer cleanupAdmissionNamespace(uid, gid)
	if err = syscall.Setresgid(gid, gid, 0); err != nil {
		return fixtureFailure(err)
	}
	if err = syscall.Setresuid(uid, uid, 0); err != nil {
		return fixtureFailure(err)
	}
	if err := os.Setenv("BRINE_HOST_GATE_FIXTURE", "ready"); err != nil {
		return fixtureFailure(err)
	}
	return m.Run()
}

func writeAdmissionFixture(raw []byte) error {
	for _, name := range []string{"state.json", "lock", "stage.lock"} {
		if err := os.WriteFile(filepath.Join(admissionRoot, name), raw, 0644); err != nil {
			return err
		} // #nosec G306 -- Exact admission metadata in the private namespace.
		if err := os.Chmod(filepath.Join(admissionRoot, name), 0644); err != nil {
			return err
		} // #nosec G302 -- Required public-readable admission metadata in the private namespace.
	}
	return nil
}

func fixtureFailure(err error) int {
	fmt.Fprintln(os.Stderr, "admission fixture:", err)
	return 1
}

func bindFixtureRoots(root string) error {
	for _, name := range []string{"usr", "etc", "work", "data", "proc", "sys", "dev", "run", "bin", "lib", "lib64"} {
		if err := bindFixtureRoot(root, name); err != nil {
			return err
		}
	}
	return nil
}

func bindFixtureRoot(root, name string) error {
	source := "/" + name
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	destination := filepath.Join(root, name)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(target, destination)
	}
	if err := os.Mkdir(destination, 0700); err != nil { // #nosec G703 -- Namespace destination comes only from the fixed root list.
		return err
	}
	return unix.Mount(source, destination, "", unix.MS_BIND|unix.MS_REC, "")
}

func bindFixtureExecutable(root string) error {
	if err := os.Mkdir(filepath.Join(root, "tmp"), os.ModeSticky|0777); err != nil { // #nosec G703 -- Namespace-private temp directory.
		return err
	} // #nosec G301 -- Private namespace temp with standard sticky metadata.
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(executable, "/tmp/") {
		return nil
	}
	destination := filepath.Join(root, filepath.Dir(executable))
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	} // #nosec G301 G703 -- Private bind target for this Go test binary's output directory.
	if filepath.Dir(executable) != "/tmp" {
		return unix.Mount(filepath.Dir(executable), destination, "", unix.MS_BIND, "")
	}
	return fmt.Errorf("put the host test binary in its own directory, not directly in /tmp")

}

func fixtureNamespaceArguments() ([]string, error) {
	prefix, err := fixtureNamespacePrefix()
	if err != nil {
		return nil, err
	}
	return append(append(prefix, os.Args[0]), os.Args[1:]...), nil
}

func fixtureNamespacePrefix() ([]string, error) {
	uid := os.Geteuid()
	arguments := []string{"--user", "--mount", "--setuid=0", "--setgid=0"}
	if uid == 0 {
		return append(arguments, "--map-root-user"), nil
	}
	identity, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil, err
	}
	rootUID, err := fixtureSubID("/etc/subuid", identity.Username)
	if err != nil {
		return nil, err
	}
	rootGID, err := fixtureSubID("/etc/subgid", identity.Username)
	if err != nil {
		return nil, err
	}
	arguments = append(arguments, "--map-current-user", "--map-users=0:"+rootUID+":1", "--map-groups=0:"+rootGID+":1")
	return arguments, nil
}

func fixtureSubID(path, name string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- Only the fixed /etc/subuid and /etc/subgid databases are read.
	if err != nil {
		return "", err
	} // #nosec G304 -- Fixed subordinate-identity database selected by the fixture launcher.
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 3 && fields[0] == name {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("host gate tests require a subordinate ID for %s", name)
}

func cleanupAdmissionNamespace(uid, gid int) {
	if err := syscall.Seteuid(0); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if executable, err := os.Executable(); err == nil && strings.HasPrefix(executable, "/tmp/") {
		_ = unix.Unmount(filepath.Dir(executable), unix.MNT_DETACH)
	}
	if identity, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		cleanupFixtureHome(identity.HomeDir)
	}

	for _, name := range []string{"var", "tmp"} {
		_ = os.RemoveAll("/" + name)
	}
	for _, name := range []string{"usr", "etc", "work", "data", "proc", "sys", "dev", "run", "bin", "lib", "lib64"} {
		if err := unix.Unmount("/"+name, unix.MNT_DETACH); err == nil {
			_ = os.Lchown("/"+name, uid, gid)
			continue
		}
		if info, err := os.Lstat("/" + name); err == nil && info.Mode()&os.ModeSymlink != 0 {
			_ = os.Lchown("/"+name, uid, gid)
		}
	}
	_ = os.Chown("/", uid, gid)
}

func bindFixtureHome(root string) error {
	identity, err := user.LookupId(os.Getenv("BRINE_HOST_GATE_UID"))
	if err != nil {
		return err
	}
	destination := filepath.Join(root, identity.HomeDir)
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	} // #nosec G301 G703 -- Protected private root with only this test user's home bound below it.
	if err := os.Chmod(filepath.Dir(destination), 0755); err != nil { // #nosec G302 G703 -- Private synthetic home ancestor, never the bound home.
		return err
	} // #nosec G302 -- Traversable private home ancestor, not the bound real home.
	return unix.Mount(identity.HomeDir, destination, "", unix.MS_BIND|unix.MS_REC, "")
}

func cleanupFixtureHome(home string) {
	if err := unix.Unmount(home, unix.MNT_DETACH); err != nil {
		fmt.Fprintln(os.Stderr, "fixture home unmount:", err)
		return
	}
	parent := filepath.Dir(home)
	if parent != "/" {
		_ = os.RemoveAll(parent)
		return
	}
	_ = os.Remove(home)
}

// A test timeout can exit before in-namespace defers run. Reclaim only the
// parent-created scratch root, from the same subordinate-UID namespace.
func removeAdmissionFixtureRoot(root string) error {
	if err := os.RemoveAll(root); err == nil {
		return nil
	}
	prefix, err := fixtureNamespacePrefix()
	if err != nil {
		return err
	}
	prefix = append(prefix, "/usr/bin/chown", "-hR", "-P", "--", strconv.Itoa(os.Geteuid())+":"+strconv.Itoa(os.Getegid()), root)
	command := exec.Command("/usr/bin/unshare", prefix...) // #nosec G204 G702 -- Reclaims only this invocation's private scratch directory.
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func protectFixtureAdmissionDirectories() error {
	for _, path := range []string{"/var", "/var/lib", admissionRoot} {
		if err := os.Chmod(path, 0755); err != nil {
			return err
		} // #nosec G302 -- Exact protected production directory metadata inside the namespace.
	}
	return nil
}
