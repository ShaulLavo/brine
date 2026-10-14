package enroll

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestEnrollmentPlansMandatoryUIDMapPackage(t *testing.T) {
	f := supported()
	delete(f.Packages, "uidmap")
	f.PackageInstall = []Package{{Name: "uidmap", Version: "1:4.17.4-2"}}
	p, err := MakePlan(f)
	if err != nil || !slices.Contains(p.MissingPackages, "uidmap") {
		t.Fatalf("missing rootless ID-map package not reviewed: missing=%v err=%v", p.MissingPackages, err)
	}
	f.PackageInstall = nil
	if _, err := MakePlan(f); err == nil {
		t.Fatal("missing mandatory uidmap admitted without an exact install preview")
	}
}

func TestMissingIDMapHelperRefusesBeforePodman(t *testing.T) {
	for _, helper := range []string{"/usr/bin/newuidmap", "/usr/bin/newgidmap"} {
		t.Run(helper, func(t *testing.T) { missingIDMapHelperRefuses(t, helper) })
	}
}

func missingIDMapHelperRefuses(t *testing.T, helper string) {
	t.Helper()
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if c.Path != "/usr/bin/test" {
			t.Fatal("Podman was reached with a known-missing mandatory helper")
		}
		if slices.Equal(c.Args, []string{"-x", helper}) {
			return localexec.Result{}, &localexec.Error{ExitCode: 1}
		}
		return localexec.Result{}, nil
	}}
	h := host{exec: f, r: hostRecord{UID: 1001}}
	err := h.checkEmptyRuntime(context.Background())
	if err == nil || !strings.Contains(err.Error(), helper) {
		t.Fatalf("missing helper did not identify the rootless prerequisite: %v", err)
	}
}

func TestProbeReviewsMissingRootlessPrerequisitesWithoutRecommendations(t *testing.T) {
	for _, name := range []string{"uidmap", "dbus-user-session"} {
		t.Run(name, func(t *testing.T) { probeMissingRootlessPrerequisite(t, name) })
	}
}

func probeMissingRootlessPrerequisite(t *testing.T, name string) {
	t.Helper()
	versions := map[string]string{"podman": "5.4.2+ds1-2+b2", "passt": "0.0~git20250503.587980c-2+deb13u1", "caddy": "2.6.2-12+deb13u1", "fuse-overlayfs": "1.14-1+b1", "uidmap": "1:4.17.4-2", "dbus-user-session": "1.16.2-2"}
	version := versions[name]
	blob := append([]byte{0, 0, 0, 11}, []byte("ssh-ed25519")...)
	blob = append(blob, 0, 0, 0, 32)
	blob = append(blob, make([]byte, 32)...)
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		switch c.Path {
		case "uname":
			return localexec.Result{Stdout: "x86_64"}, nil
		case "dpkg-query":
			if c.Args[len(c.Args)-1] == name {
				return localexec.Result{Stdout: "not-installed\t"}, nil
			}
			return localexec.Result{Stdout: "installed\t" + versions[c.Args[len(c.Args)-1]]}, nil
		case "apt-get":
			if !slices.Equal(c.Args, []string{"--simulate", "--no-remove", "--no-install-recommends", "install", name}) || c.Mutation {
				t.Fatalf("unreviewed package probe: %+v", c)
			}
			return localexec.Result{Stdout: "Inst " + name + " (" + version + " Debian:13/stable [amd64])\n"}, nil
		default:
			return localexec.Result{}, &localexec.Error{ExitCode: 1}
		}
	}}
	p := Prober{FS: configFS{files: map[string]string{
		"/etc/os-release":                   "ID=debian\nVERSION_ID=13\n",
		"/etc/machine-id":                   strings.Repeat("1", 32),
		"/etc/ssh/ssh_host_ed25519_key.pub": "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob),
	}}, Runner: probeRunner{f}, IdentityKey: make([]byte, 32)}
	facts, err := p.Collect(context.Background())
	// The later SSH audit intentionally lacks its fixture; package probing must
	// already have bound the exact mandatory dependency by then.
	if err == nil || !slices.Equal(facts.PackageInstall, []Package{{Name: name, Version: version}}) || len(facts.Packages) != 5 {
		t.Fatalf("missing %s did not enter the exact package preview: %v %v", name, facts.PackageInstall, err)
	}
}

func TestUIDMapInstallUsesOriginalJournalVersion(t *testing.T) {
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if c.Path != "apt-get" || !slices.Equal(c.Args[1:], []string{"--no-remove", "--no-install-recommends", "install", "uidmap=1:4.17.4-2"}) {
			t.Fatalf("uidmap install lost reviewed version: %+v", c)
		}
		return localexec.Result{Stdout: "Inst uidmap (1:4.17.4-2 Debian:13/stable [amd64])\n"}, nil
	}}
	h := host{exec: f, r: hostRecord{Packages: []Package{{Name: "uidmap", Version: "1:4.17.4-2"}}}}
	if err := h.installPackages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 2 || f.commands[0].Mutation || !f.commands[1].Mutation {
		t.Fatal("installation bypassed read-only exact preview")
	}
}
