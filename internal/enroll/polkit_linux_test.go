package enroll

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestFreshEnrollmentRequiresReviewedPolkitDaemon(t *testing.T) {
	f := supported()
	delete(f.Packages, "polkitd")
	f.PackageInstall = []Package{{Name: "polkitd", Version: "126-2"}}
	p, err := MakePlan(f)
	if err != nil || !slices.Equal(p.MissingPackages, []string{"polkitd"}) {
		t.Fatalf("missing polkit daemon did not enter the reviewed plan: %v %v", p.MissingPackages, err)
	}
	if !strings.Contains(strings.Join(p.Changes, "\n"), "polkitd") {
		t.Fatal("polkit daemon omitted from operator-visible changes")
	}
	f.PackageInstall = nil
	if _, err := MakePlan(f); err == nil {
		t.Fatal("missing polkit daemon admitted without an exact transaction")
	}
	for _, version := range []string{"125-2", "127-1", "1260-1"} {
		f.PackageInstall = []Package{{Name: "polkitd", Version: version}}
		if _, err := MakePlan(f); err == nil {
			t.Fatalf("unqualified polkit daemon package %q admitted", version)
		}
	}
}

func TestPreinstalledPolkitDaemonNeedsNoTransaction(t *testing.T) {
	f := supported()
	f.Packages["polkitd"] = "126-2"
	p, err := MakePlan(f)
	if err != nil || len(p.MissingPackages) != 0 {
		t.Fatalf("installed polkit daemon scheduled for installation: %v %v", p.MissingPackages, err)
	}
}

func TestMinimalDebianEnrollmentPlansAllMandatoryPackages(t *testing.T) {
	f := supported()
	f.Packages = map[string]string{}
	f.PackageInstall = []Package{
		{Name: "podman", Version: "5.4.2+ds1-2+b2"},
		{Name: "passt", Version: "0.0~git20250503.587980c-2+deb13u1"},
		{Name: "caddy", Version: "2.6.2-12+deb13u1"},
		{Name: "uidmap", Version: "1:4.17.4-2"},
		{Name: "fuse-overlayfs", Version: "1.14-1+b1"},
		{Name: "dbus-user-session", Version: "1.16.2-2"},
		{Name: "polkitd", Version: "126-2"},
	}
	p, err := MakePlan(f)
	if err != nil || !slices.Equal(p.MissingPackages, requiredPackages) || !slices.Contains(p.MissingPackages, "polkitd") {
		t.Fatalf("minimal Debian omitted a mandatory package: %v %v", p.MissingPackages, err)
	}
}

func TestPolkitInstallUsesReviewedVersionAndDependencies(t *testing.T) {
	transaction := "Inst libpolkit-agent-1-0 (126-2 Debian:13/stable [amd64])\nInst polkitd (126-2 Debian:13/stable [amd64])\n"
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if c.Path != "apt-get" || !slices.Equal(c.Args[1:], []string{"--no-remove", "--no-install-recommends", "install", "libpolkit-agent-1-0=126-2", "polkitd=126-2"}) {
			t.Fatalf("polkit install changed the reviewed transaction: %+v", c)
		}
		return localexec.Result{Stdout: transaction}, nil
	}}
	h := host{exec: f, r: hostRecord{Packages: []Package{{Name: "libpolkit-agent-1-0", Version: "126-2"}, {Name: "polkitd", Version: "126-2"}}}}
	if err := h.installPackages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 2 || f.commands[0].Mutation || !f.commands[1].Mutation {
		t.Fatal("polkit installation bypassed the exact read-only preview")
	}
}
