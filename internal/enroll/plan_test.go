package enroll

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func supported() Facts {
	return Facts{Snapshot: target.Snapshot{OS: target.OS{ID: "debian", Version: "13"}, Arch: "arm64", CgroupV2: target.Known(true), Versions: target.Versions{Systemd: target.Known("257"), Podman: target.Known("5.4.2"), Caddy: target.Known("2.6.2"), Passt: target.Known("0.0~git20250503.fixture")}, Runner: target.Runner{User: target.Observation[string]{Status: target.Absent}}, PortOwners: target.Known([]target.PortOwner{}), UsedPorts: target.Known([]target.Port{})}, PermitUserEnvironment: "no", PAMChecked: true, HostKey: "synthetic", Packages: map[string]string{"podman": "5.4", "passt": "1", "caddy": "2.6"}}
}
func TestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*Facts)
	}{
		{"unsupported runtime", func(f *Facts) { f.Snapshot.Versions.Podman = target.Known("9.0") }},
		{"OS", func(f *Facts) { f.Snapshot.OS.ID = "other" }},
		{"arch", func(f *Facts) { f.Snapshot.Arch = "other" }},
		{"unknown ports", func(f *Facts) { f.Snapshot.PortOwners = target.Observation[[]target.PortOwner]{Status: target.Unknown} }},
		{"proxy", func(f *Facts) {
			f.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 443, Process: "nginx"}})
		}},
		{"unowned user", func(f *Facts) { f.Snapshot.Runner.User = target.Known("brine") }},
		{"environment", func(f *Facts) { f.PermitUserEnvironment = "yes" }},
		{"PAM env", func(f *Facts) { f.PAMUserEnvironment = true }},
		{"unknown PAM", func(f *Facts) { f.PAMChecked = false }},
		{"cgroup", func(f *Facts) { f.Snapshot.CgroupV2 = target.Known(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := supported()
			tc.alter(&f)
			if _, err := MakePlan(f); err == nil {
				t.Fatal("accepted unsafe facts")
			}
		})
	}
}
func TestPlanFixedChanges(t *testing.T) {
	p, err := MakePlan(supported())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(p.Changes, "\n")
	for _, want := range []string{"mask", "netavark", "/bin/sh", "authorized_keys", "reload", "host key", "no firewall", "startup", "root-owned"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
}
func TestConfirmation(t *testing.T) {
	for _, tc := range []struct {
		terminal, noInput bool
		input             string
		want              bool
	}{
		{false, false, "fixture\n", false}, {true, true, "fixture\n", false}, {true, false, "yes\n", false}, {true, false, "fixture\n", true}, {true, false, "fixture\nextra\n", true},
	} {
		err := Confirm(strings.NewReader(tc.input), tc.terminal, tc.noInput, "fixture")
		if (err == nil) != tc.want {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestDefaultTargetName(t *testing.T) {
	for _, tt := range []struct{ destination, name string }{{"root@fixture.example", "fixture-example"}, {"fixture_alias", "fixture-alias"}, {"operator@Fixture", "fixture"}, {"9-fixture", "host-9-fixture"}} {
		if got := DefaultTargetName(tt.destination); got != tt.name {
			t.Errorf("%s %s", tt.destination, got)
		}
	}
}

func TestMissingRuntimeCandidatesMustBeSupported(t *testing.T) {
	for _, tt := range []struct{ name, good, bad string }{{"podman", "5.4.2+ds1-2+b2", "9.0-1"}, {"caddy", "2.6.2-12+deb13u1", "2.10.0-1"}, {"passt", "0.0~git20250503.587980c-2", "0.0~git20260101.fixture"}} {
		t.Run(tt.name, func(t *testing.T) {
			f := supported()
			delete(f.Packages, tt.name)
			for _, version := range []string{tt.bad, "", tt.good} {
				f.PackageInstall = []Package{{Name: tt.name, Version: version}}
				_, err := MakePlan(f)
				if (err == nil) != (version == tt.good) {
					t.Fatalf("candidate %q: %v", version, err)
				}
			}
			f.PackageInstall = nil
			if _, err := MakePlan(f); err == nil {
				t.Fatal("unbound missing package accepted")
			}
		})
	}
}
