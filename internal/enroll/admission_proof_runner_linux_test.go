//go:build linux

package enroll

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestAdmissionProofSyntheticRunnerFiles(t *testing.T) {
	cases := map[string]func(*testing.T, AdmissionProofs, AdmissionProofContext){
		"linger present": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			proofWrite(t, filepath.Join(p.Root, "var/lib/systemd/linger/brine"), "")
		},
		"linger dangling link": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			proofLink(t, filepath.Join(p.Root, "missing"), filepath.Join(p.Root, "var/lib/systemd/linger/brine"))
		},
		"linger parent missing": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			proofRemove(t, filepath.Join(p.Root, "var/lib/systemd/linger"))
		},
		"linger parent unreadable": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			if err := os.Chmod(filepath.Join(p.Root, "var/lib/systemd/linger"), 0); err != nil {
				t.Fatal(err)
			}
		},
		"cgroup process": func(t *testing.T, p AdmissionProofs, b AdmissionProofContext) {
			proofWrite(t, fixtureCgroup(p, b)+"/cgroup.procs", "123\n")
		},
		"child cgroup process": func(t *testing.T, p AdmissionProofs, b AdmissionProofContext) {
			proofWrite(t, fixtureCgroup(p, b)+"/child/cgroup.procs", "123\n")
		},
		"cgroup missing": func(t *testing.T, p AdmissionProofs, b AdmissionProofContext) {
			proofRemove(t, fixtureCgroup(p, b)+"/cgroup.procs")
			proofRemove(t, fixtureCgroup(p, b))
		},
		"cgroup procs missing": func(t *testing.T, p AdmissionProofs, b AdmissionProofContext) {
			proofRemove(t, fixtureCgroup(p, b)+"/cgroup.procs")
		},
		"cgroup procs unreadable": func(t *testing.T, p AdmissionProofs, b AdmissionProofContext) {
			if err := os.Chmod(fixtureCgroup(p, b)+"/cgroup.procs", 0); err != nil {
				t.Fatal(err)
			}
		},
		"cgroup link": func(t *testing.T, p AdmissionProofs, b AdmissionProofContext) {
			proofLink(t, p.Root, fixtureCgroup(p, b)+"/child")
		},
		"cgroup oversized": func(t *testing.T, p AdmissionProofs, b AdmissionProofContext) {
			proofWrite(t, fixtureCgroup(p, b)+"/cgroup.procs", strings.Repeat(" ", 65537))
		},
		"mountinfo missing": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) { proofRemove(t, p.MountInfo) },
		"mountinfo unreadable": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			if err := os.Chmod(p.MountInfo, 0); err != nil {
				t.Fatal(err)
			}
		},
		"mountinfo symlink": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			proofRemove(t, p.MountInfo)
			proofLink(t, "/proc/self/mountinfo", p.MountInfo)
		},
		"mountinfo oversized": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			proofWrite(t, p.MountInfo, strings.Repeat(" ", p.limits.bytes+1))
		},
		"PAM missing": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			proofRemove(t, filepath.Join(p.Root, "etc/pam.d/runuser"))
		},
		"PAM oversized": func(t *testing.T, p AdmissionProofs, _ AdmissionProofContext) {
			proofWrite(t, filepath.Join(p.Root, "etc/pam.d/runuser"), strings.Repeat("#", 16385))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p, b, _ := newSyntheticProof(t)
			setup(t, p, b)
			requireProofRefusal(t, p, b, true)
		})
	}
}

func fixtureCgroup(p AdmissionProofs, b AdmissionProofContext) string {
	return filepath.Join(p.CgroupRoot, "user.slice", "user-"+strconv.Itoa(b.RunnerUID)+".slice")
}

type proofCommandCase struct {
	name, path, out string
	code            int
	err             error
	truncated       bool
}

func TestAdmissionProofSyntheticRunnerCommands(t *testing.T) {
	cases := []proofCommandCase{
		{name: "account absent", path: "getent passwd", code: 2, err: &localexec.Error{Kind: localexec.Failed, ExitCode: 2}},
		{name: "foreign account", path: "getent passwd", out: "brine:x:2001:2001:other:/home/brine:/bin/sh\n"},
		{name: "expiry absent", path: "getent shadow", out: "brine:!:20000:0:99999:7:::\n"},
		{name: "expiry wrong", path: "getent shadow", out: "brine:!:20000:0:99999:7::2:\n"},
		{name: "expiry future", path: "getent shadow", out: "brine:!:20000:0:99999:7::99999:\n"},
		{name: "shadow malformed", path: "getent shadow", out: "brine:!:1\n"},
		{name: "shadow duplicate", path: "getent shadow", out: strings.Repeat("brine:!:20000:0:99999:7::1:\n", 2)},
		{name: "shadow truncated", path: "getent shadow", out: "brine:!:20000:0:99999:7::1:"},
		{name: "manager active", path: "systemctl", out: "LoadState=loaded\nActiveState=active\nSubState=running\n"},
		{name: "manager failed", path: "systemctl", out: "LoadState=loaded\nActiveState=failed\nSubState=failed\n"},
		{name: "manager activating", path: "systemctl", out: "LoadState=loaded\nActiveState=activating\nSubState=start\n"},
		{name: "manager unloaded", path: "systemctl", out: "LoadState=not-found\nActiveState=inactive\nSubState=dead\n"},
		{name: "manager incomplete", path: "systemctl", out: "ActiveState=inactive\n"},
		{name: "manager duplicate", path: "systemctl", out: "LoadState=loaded\nActiveState=inactive\nActiveState=inactive\n"},
		{name: "manager failed query", path: "systemctl", code: 1, err: errors.New("fixture command failure")},
		{name: "pgrep live", path: "pgrep", out: "123\n"},
		{name: "pgrep empty zero", path: "pgrep"},
		{name: "pgrep code two", path: "pgrep", code: 2, err: &localexec.Error{Kind: localexec.Failed, ExitCode: 2}},
		{name: "pgrep error", path: "pgrep", code: 1, err: errors.New("fixture unavailable")},
		{name: "pgrep nonempty one", path: "pgrep", out: "123\n", code: 1, err: &localexec.Error{Kind: localexec.Failed, ExitCode: 1}},
		{name: "SSH PAM off", path: "/usr/sbin/sshd", out: "usepam no\n"},
		{name: "SSH PAM missing", path: "/usr/sbin/sshd", out: "port 22\n"},
		{name: "SSH PAM duplicate", path: "/usr/sbin/sshd", out: "usepam yes\nusepam yes\n"},
		{name: "SSH failed", path: "/usr/sbin/sshd", err: errors.New("fixture ssh failure")},
		{name: "truncated capture", path: "getent passwd", out: "partial\n", truncated: true},
		{name: "oversized capture", path: "getent passwd", out: strings.Repeat("x", 16385)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { testSyntheticRunnerCommand(t, tc) })
	}
}

func testSyntheticRunnerCommand(t *testing.T, tc proofCommandCase) {
	t.Helper()
	p, b, e := newSyntheticProof(t)
	e.override = func(c localexec.Command) (localexec.Result, error, bool) {
		match := c.Path
		if c.Path == "getent" {
			match += " " + c.Args[0]
		}
		return localexec.Result{Stdout: tc.out, ExitCode: tc.code, Truncated: tc.truncated}, tc.err, match == tc.path
	}
	requireProofRefusal(t, p, b, true)
}

func TestAdmissionProofSyntheticRunnerUIDs(t *testing.T) {
	for i := range 4 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			p, b, _ := newSyntheticProof(t)
			base := syntheticProcess(t, p, "123", b.RunnerUID+1)
			uids := []string{"12", "12", "12", "12"}
			uids[i] = strconv.Itoa(b.RunnerUID)
			proofWrite(t, filepath.Join(base, "status"), "Uid: "+strings.Join(uids, " ")+"\n")
			requireProofRefusal(t, p, b, true)
		})
	}
}

func TestAdmissionProofSyntheticPAM(t *testing.T) {
	for _, raw := range []string{"", "auth sufficient pam_rootok.so", "account required pam_unix.so\n", "-account required pam_unix.so\n", "@include common-account\n", "auth\n", "auth required pam_rootok.so \\\naccount required pam_unix.so\n"} {
		t.Run(raw, func(t *testing.T) {
			p, b, _ := newSyntheticProof(t)
			proofWrite(t, filepath.Join(p.Root, "etc/pam.d/runuser"), raw)
			requireProofRefusal(t, p, b, true)
		})
	}
}

func TestAdmissionProofSyntheticMounts(t *testing.T) {
	for _, mount := range []string{home, home + "/child", "/etc/caddy/brine", "/etc/caddy/brine/child", `/home/brine/child\040name`} {
		t.Run(mount, func(t *testing.T) {
			p, b, _ := newSyntheticProof(t)
			proofWrite(t, p.MountInfo, "1 0 0:1 / "+mount+" rw - tmpfs tmpfs rw\n")
			requireProofRefusal(t, p, b, true)
		})
	}
	for _, raw := range []string{"", "1 0 0:1 / / rw - tmpfs tmpfs rw", "1 0 / / rw - tmpfs tmpfs rw\n", "x 0 0:1 / / rw - tmpfs tmpfs rw\n", `1 0 0:1 / /bad\999 rw - tmpfs tmpfs rw` + "\n", "1 0 0:1 / /home/brine/../else rw - tmpfs tmpfs rw\n", "1 0 0:1 / / rw - tmpfs\n"} {
		if proofMounts(raw) {
			t.Fatalf("malformed mountinfo accepted %q", raw)
		}
	}
	if !proofMounts("1 0 0:1 / /home/brine-other rw - tmpfs tmpfs rw\n2 1 0:2 / /etc/caddy/brine-other rw - tmpfs tmpfs rw\n") {
		t.Fatal("path component prefix refused")
	}
}
