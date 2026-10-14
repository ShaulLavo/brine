package enroll

import (
	"context"
	"slices"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestEnrollmentRequiresReviewedUserBusPackage(t *testing.T) {
	f := supported()
	delete(f.Packages, "dbus-user-session")
	f.PackageInstall = []Package{{Name: "dbus-user-session", Version: "1.16.2-2"}}
	p, err := MakePlan(f)
	if err != nil || !slices.Equal(p.MissingPackages, []string{"dbus-user-session"}) {
		t.Fatalf("missing user bus did not enter the reviewed plan: %v %v", p.MissingPackages, err)
	}
	f.PackageInstall = nil
	if _, err = MakePlan(f); err == nil {
		t.Fatal("missing user bus admitted without a package transaction")
	}
	for _, version := range []string{"1.14.10-1", "2.0-1"} {
		f.PackageInstall = []Package{{Name: "dbus-user-session", Version: version}}
		if _, err = MakePlan(f); err == nil {
			t.Fatalf("unqualified user bus package version %s admitted", version)
		}
	}
}

func TestUserBusInstallUsesOriginalJournalVersionAndDependencies(t *testing.T) {
	transaction := "Inst dbus-daemon (1.16.2-2 Debian:13/stable [amd64])\nInst dbus-user-session (1.16.2-2 Debian:13/stable [amd64])\n"
	f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
		if c.Path != "apt-get" || !slices.Equal(c.Args[1:], []string{"--no-remove", "--no-install-recommends", "install", "dbus-daemon=1.16.2-2", "dbus-user-session=1.16.2-2"}) {
			t.Fatalf("user bus install changed the reviewed versions: %+v", c)
		}
		return localexec.Result{Stdout: transaction}, nil
	}}
	h := host{exec: f, r: hostRecord{Packages: []Package{{Name: "dbus-daemon", Version: "1.16.2-2"}, {Name: "dbus-user-session", Version: "1.16.2-2"}}}}
	if err := h.installPackages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 2 || f.commands[0].Mutation || !f.commands[1].Mutation {
		t.Fatal("user bus installation bypassed the exact read-only preview")
	}
}
