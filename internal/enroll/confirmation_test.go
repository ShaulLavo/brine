package enroll

import (
	"github.com/ShaulLavo/brine/internal/target"
	"testing"
)

func TestConfirmedTransactionAndFactsFailClosedOnDrift(t *testing.T) {
	before := supported()
	delete(before.Packages, "podman")
	before.PackageInstall = []Package{{"podman", "5.4.2+ds1-2"}, {"fixture-dependency", "1.0"}}
	binding, err := confirmationBinding(before)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkConfirmation(before, binding); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"candidate", "dependency", "host", "installed", "missing"} {
		t.Run(name, func(t *testing.T) {
			f := supported()
			delete(f.Packages, "podman")
			f.PackageInstall = append([]Package{}, before.PackageInstall...)
			switch name {
			case "candidate":
				f.PackageInstall[0].Version = "5.4.3-1"
			case "dependency":
				f.PackageInstall = append(f.PackageInstall, Package{"additional", "1.0"})
			case "host":
				f.HostKey = "different"
			case "installed":
				f.Packages["caddy"] = "2.6.2-13"
			case "missing":
				binding = ""
			}
			if err := checkConfirmation(f, binding); err == nil {
				t.Fatal("unconfirmed drift accepted")
			}
		})
	}
}

func TestConfirmationIgnoresUnrelatedListenerChanges(t *testing.T) {
	f := supported()
	binding, err := confirmationBinding(f)
	if err != nil {
		t.Fatal(err)
	}
	f.Snapshot.UsedPorts = target.Known([]target.Port{12345})
	f.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 12345, Process: "fixture", Unit: "transient.scope"}})
	if err := checkConfirmation(f, binding); err != nil {
		t.Fatal("unrelated listener invalidated confirmation:", err)
	}
	f.Snapshot.UsedPorts = target.Known([]target.Port{80})
	f.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 80, Process: "caddy", Unit: "caddy.service"}})
	if err := checkConfirmation(f, binding); err == nil {
		t.Fatal("relevant listener drift accepted")
	}
}
