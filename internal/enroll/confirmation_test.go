package enroll

import "testing"

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
