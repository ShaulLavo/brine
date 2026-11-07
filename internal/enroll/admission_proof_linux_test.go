//go:build linux

package enroll

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type proofExecutor struct {
	binding  AdmissionProofContext
	override func(localexec.Command) (localexec.Result, error, bool)
	calls    []localexec.Command
}

func (e *proofExecutor) Execute(ctx context.Context, command localexec.Command) (localexec.Result, error) {
	e.calls = append(e.calls, command)
	if err := ctx.Err(); err != nil {
		return localexec.Result{}, err
	}
	if command.Mutation || command.Timeout <= 0 || command.OutputLimit != 16384 {
		return localexec.Result{}, errors.New("unsafe proof command")
	}
	if e.override != nil {
		out, err, handled := e.override(command)
		if handled {
			return out, err
		}
	}
	switch command.Path + " " + strings.Join(command.Args, " ") {
	case "getent passwd brine":
		return localexec.Result{Stdout: fmt.Sprintf("brine:x:%d:%d:brine-enrollment-%s:/home/brine:/bin/sh\n", e.binding.RunnerUID, os.Getegid(), e.binding.EnrollmentID)}, nil
	case "getent shadow brine":
		return localexec.Result{Stdout: "brine:!:20000:0:99999:7::1:\n"}, nil
	case "pgrep -u " + strconv.Itoa(e.binding.RunnerUID):
		return localexec.Result{ExitCode: 1}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
	case "systemctl show user@" + strconv.Itoa(e.binding.RunnerUID) + ".service --property=LoadState,ActiveState,SubState":
		return localexec.Result{Stdout: "LoadState=loaded\nActiveState=inactive\nSubState=dead\n"}, nil
	}
	if command.Path == "/usr/sbin/sshd" && len(command.Args) == 3 && command.Args[0] == "-T" && command.Args[1] == "-C" {
		return localexec.Result{Stdout: "usepam yes\n"}, nil
	}
	return localexec.Result{}, errors.New("unexpected proof command")
}

// All proc, sys and mount observations here are synthetic fixtures. Live kernel
// fd tests are separate and never assert successful quiescence of the host.
func newSyntheticProof(t *testing.T) (AdmissionProofs, AdmissionProofContext, *proofExecutor) {
	t.Helper()
	root := t.TempDir()
	binding := AdmissionProofContext{EnrollmentID: strings.Repeat("a", 32), TransactionID: strings.Repeat("b", 32), RunnerUID: os.Geteuid(), FenceExpiryDays: 1}
	if binding.RunnerUID == 0 {
		t.Skip("fixture requires unprivileged owner")
	}
	for _, name := range []string{admissionState, "proc", "sys/fs/cgroup/user.slice/user-" + strconv.Itoa(binding.RunnerUID) + ".slice", "var/lib/systemd/linger"} {
		proofMkdir(t, filepath.Join(root, name))
	}
	proofWrite(t, filepath.Join(root, "etc/pam.d/runuser"), "auth sufficient pam_rootok.so\nsession required pam_unix.so\n")
	proofWrite(t, filepath.Join(root, "sys/fs/cgroup/user.slice/user-"+strconv.Itoa(binding.RunnerUID)+".slice/cgroup.procs"), "")
	proofWrite(t, filepath.Join(root, "mountinfo"), "1 0 0:1 / / rw - tmpfs tmpfs rw\n")
	executor := &proofExecutor{binding: binding}
	return AdmissionProofs{Root: root, ProcRoot: filepath.Join(root, "proc"), CgroupRoot: filepath.Join(root, "sys/fs/cgroup"), MountInfo: filepath.Join(root, "mountinfo"), Executor: executor}.bounded(), binding, executor
}

func proofMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func proofWrite(t *testing.T, path, data string) {
	t.Helper()
	proofMkdir(t, filepath.Dir(path))
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := root.WriteFile(filepath.Base(path), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func proofRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func syntheticProcess(t *testing.T, p AdmissionProofs, pid string, uid int) string {
	t.Helper()
	base := filepath.Join(p.ProcRoot, pid)
	proofMkdir(t, filepath.Join(base, "fd"))
	// Full proc stat fields, including a command containing a closing parenthesis.
	proofWrite(t, filepath.Join(base, "stat"), pid+" (fixture ) process) S "+strings.Repeat("0 ", 18)+"123 "+strings.TrimSpace(strings.Repeat("0 ", 30))+"\n")
	proofWrite(t, filepath.Join(base, "status"), fmt.Sprintf("Name:\tfixture\nUid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid))
	return base
}

func proofLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func requireProofRefusal(t *testing.T, p AdmissionProofs, b AdmissionProofContext, runner bool) {
	t.Helper()
	var result AdmissionQuiescenceProof
	var err error
	if runner {
		result, err = p.ProveRunnerQuiescent(t.Context(), b)
	} else {
		result, err = p.ProveStoreQuiescent(t.Context(), b)
	}
	if err == nil || result != (AdmissionQuiescenceProof{}) {
		t.Fatalf("expected refusal without proof, got %+v, %v", result, err)
	}
}

func TestAdmissionProofSyntheticEmpty(t *testing.T) {
	p, b, executor := newSyntheticProof(t)
	for _, runner := range []bool{false, true} {
		proof, err := p.ProveStoreQuiescent(t.Context(), b)
		if runner {
			proof, err = p.ProveRunnerQuiescent(t.Context(), b)
		}
		if err != nil || proof.Context != b || proof.Runner != runner || proof.Processes != 0 || proof.Descriptors != 0 {
			t.Fatalf("empty synthetic proof %+v, %v", proof, err)
		}
	}
	if len(executor.calls) != 16 {
		t.Fatalf("runner fence not repeated, calls %d", len(executor.calls))
	}
}

func TestAdmissionProofSyntheticStoreHolders(t *testing.T) {
	for _, name := range admissionStoreNames {
		t.Run(name, func(t *testing.T) {
			p, b, _ := newSyntheticProof(t)
			path := filepath.Join(p.Root, admissionState, name)
			proofWrite(t, path, "fixture")
			base := syntheticProcess(t, p, "123", b.RunnerUID+1)
			proofLink(t, path, filepath.Join(base, "fd/9"))
			requireProofRefusal(t, p, b, false)
		})
	}
}

func TestAdmissionProofSyntheticStoreNegatives(t *testing.T) {
	cases := map[string]func(*testing.T, *AdmissionProofs, AdmissionProofContext){
		"missing state": func(t *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			proofRemove(t, filepath.Join(p.Root, admissionState))
		},
		"state mode": func(t *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			// #nosec G302 -- Deliberately public test-owned directory must be refused.
			if err := os.Chmod(filepath.Join(p.Root, admissionState), 0755); err != nil {
				t.Fatal(err)
			}
		},
		"state symlink": func(t *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			path := filepath.Join(p.Root, admissionState)
			proofRemove(t, path)
			proofLink(t, p.Root, path)
		},
		"store symlink": func(t *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			proofLink(t, p.Root, filepath.Join(p.Root, admissionState, "control.db"))
		},
		"store directory": func(t *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			proofMkdir(t, filepath.Join(p.Root, admissionState, "control.db"))
		},
		"store mode": func(t *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			path := filepath.Join(p.Root, admissionState, "control.db")
			proofWrite(t, path, "")
			// #nosec G302 -- Deliberately readable test-owned store must be refused.
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
		},
		"store hardlink": func(t *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			path := filepath.Join(p.Root, admissionState, "control.db")
			proofWrite(t, path, "")
			if err := os.Link(path, filepath.Join(p.Root, "alias")); err != nil {
				t.Fatal(err)
			}
		},
		"missing proc": func(_ *testing.T, p *AdmissionProofs, _ AdmissionProofContext) {
			p.ProcRoot = filepath.Join(p.Root, "missing")
		},
		"missing status": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofRemove(t, filepath.Join(base, "status"))
		},
		"missing fd": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofRemove(t, filepath.Join(base, "fd"))
		},
		"unreadable fd": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			if err := os.Chmod(filepath.Join(base, "fd"), 0); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable status": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			if err := os.Chmod(filepath.Join(base, "status"), 0); err != nil {
				t.Fatal(err)
			}
		},
		"malformed stat": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofWrite(t, filepath.Join(base, "stat"), "123 (truncated) S 0\n")
		},
		"oversized stat": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofWrite(t, filepath.Join(base, "stat"), strings.Repeat("x", 16385))
		},
		"malformed UID": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofWrite(t, filepath.Join(base, "status"), "Uid: 1 1 nope 1\n")
		},
		"duplicate UID": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofWrite(t, filepath.Join(base, "status"), "Uid: 1 1 1 1\nUid: 1 1 1 1\n")
		},
		"truncated status": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofWrite(t, filepath.Join(base, "status"), "Uid: 1 1 1 1")
		},
		"broken descriptor": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofLink(t, filepath.Join(p.Root, "missing"), filepath.Join(base, "fd/1"))
		},
		"nonlink descriptor": func(t *testing.T, p *AdmissionProofs, b AdmissionProofContext) {
			base := syntheticProcess(t, *p, "123", b.RunnerUID+1)
			proofWrite(t, filepath.Join(base, "fd/1"), "")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p, b, _ := newSyntheticProof(t)
			setup(t, &p, b)
			requireProofRefusal(t, p, b, false)
		})
	}
}
