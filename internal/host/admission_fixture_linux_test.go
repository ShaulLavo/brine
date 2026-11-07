//go:build linux

package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The fixed production admission path is real, root-owned and isolated by a
// user/mount namespace. No production path or owner override is needed.
func TestMain(m *testing.M) { os.Exit(runAdmissionFixture(m)) }

func runAdmissionFixture(m *testing.M) int {
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
	defer func() { _ = os.RemoveAll(root) }()
	command := exec.Command(os.Args[0], os.Args[1:]...) // #nosec G204 G702 -- Re-execute only this test binary and its own arguments.
	command.Env = append(os.Environ(), "BRINE_HOST_GATE_FIXTURE=enter", "BRINE_HOST_GATE_ROOT="+root)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
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
	if err = bindFixtureRoots(root); err != nil {
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
	if err := writeAdmissionFixture([]byte(`{"version":1,"open":true}`)); err != nil {
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
	}
	return nil
}

func fixtureFailure(err error) int {
	fmt.Fprintln(os.Stderr, "admission fixture:", err)
	return 1
}

func bindFixtureRoots(root string) error {
	for _, name := range []string{"usr", "etc", "home", "work", "data", "proc", "sys", "dev", "run", "bin", "lib", "lib64"} {
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
	destination := filepath.Join(root, executable)
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil { // #nosec G301 G703 -- Bind target for this test executable in its disposable root.
		return err
	} // #nosec G301 -- Test-binary parents inside disposable root.
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) // #nosec G304 G703 -- Namespace-private destination derived from this binary.
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return unix.Mount(executable, destination, "", unix.MS_BIND, "")
}
