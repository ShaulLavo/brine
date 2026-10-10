package inventory

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func retainedFixture(t *testing.T) (Collector, fakeRunner, plan.Input) {
	t.Helper()
	f := baseFixture()
	f.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/sh\n"
	f.files["/proc/self/status"] = "Uid:\t1001\t1001\t1001\t1001\n"
	f.dirs["/home/brine/.config/containers/systemd"] = []os.DirEntry{}
	r := fakeRunner{"uname -m": "aarch64", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "fixture-id brine.fixture.fixture-token.v1", "podman --remote=false ps --all --format {{.Names}} {{.Label \"PODMAN_SYSTEMD_UNIT\"}}": "", "ss -H -ltnpe": "", "ss -H -lunp": ""}
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	ready.Generation = target.Known(uint64(2))
	image := plan.Image{ManifestDigest: target.Known("sha256:" + strings.Repeat("c", 64)), Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}}
	d := policy.Desired{SchemaVersion: 1, Name: "fixture", Image: spec.ImageReference("registry.example.test/api@" + image.Digest), ContainerPort: 8080, Domains: []spec.Domain{"fixture.example.test"}, Environment: []policy.Environment{}, Secrets: []policy.Secret{{Name: "TOKEN", Reference: "fixture-token"}}, PolicyVersion: "fixture", PolicyHash: "sha256:" + strings.Repeat("b", 64), AppPorts: policy.PortRange{Min: 20000, Max: 20010}}
	c := Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}
	fresh, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ready.Identity = fresh.Identity
	c.StateInventory = func(context.Context) (target.ControlInventory, error) {
		return target.ControlInventory{Generation: 2, Target: &fresh.Identity, Apps: []target.ControlApp{{Name: "fixture", Status: target.Absent, RetiredPorts: []target.Port{20000}}}}, nil
	}
	return c, r, plan.Input{Desired: d, Snapshot: ready, Image: image, State: plan.BrineState{Target: ready.Identity, Generation: 2, Releases: []plan.CurrentRelease{}}}
}

func TestRetainedSecretRecreation(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			c, r, in := retainedFixture(t)
			if version == "v2" {
				r["podman --remote=false secret ls --format {{.ID}} {{.Name}}"] += "\nnew-id brine.fixture.fixture-token.v2"
			}
			collected, err := c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			in.Snapshot.Apps = collected.Apps
			p, err := plan.Build(in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != plan.Create || len(p.Secrets) != 1 || p.Secrets[0].VersionName != "brine.fixture.fixture-token."+version {
				t.Fatalf("recreation failed: %#v", p.Conflicts)
			}
		})
	}
}

func TestRetainedSecretDoesNotReserveDifferentAppPort(t *testing.T) {
	for _, secrets := range []bool{true, false} {
		t.Run(map[bool]string{true: "retained secret", false: "retained history only"}[secrets], func(t *testing.T) {
			c, r, in := retainedFixture(t)
			if !secrets {
				r["podman --remote=false secret ls --format {{.ID}} {{.Name}}"] = ""
			}
			collected, err := c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			in.Snapshot.Apps = collected.Apps
			in.Desired.Name = "fixture-two"
			in.Desired.Secrets = []policy.Secret{}
			p, err := plan.Build(in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != plan.Create || p.HostPort != 20000 {
				t.Fatalf("different app blocked: %#v", p.Conflicts)
			}
		})
	}
}

func TestRetainedAbsenceRequiresAffirmativeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Collector, fakeRunner)
	}{
		{"release head without artifacts", func(c *Collector, _ fakeRunner) {
			c.StateInventory = func(context.Context) (target.ControlInventory, error) {
				return target.ControlInventory{Generation: 2, Apps: []target.ControlApp{{Name: "fixture", Status: target.KnownStatus}}}, nil
			}
		}},
		{"unfinished removal", func(c *Collector, _ fakeRunner) {
			c.StateInventory = func(context.Context) (target.ControlInventory, error) {
				return target.ControlInventory{Generation: 2, Apps: []target.ControlApp{{Name: "fixture", Status: target.Unknown}}}, nil
			}
		}},
		{"unreadable state", func(c *Collector, _ fakeRunner) {
			c.StateInventory = func(context.Context) (target.ControlInventory, error) {
				return target.ControlInventory{}, os.ErrPermission
			}
		}},
		{"retirement belongs to another target", func(c *Collector, _ fakeRunner) {
			read := c.StateInventory
			c.StateInventory = func(ctx context.Context) (target.ControlInventory, error) {
				state, err := read(ctx)
				state.Target = &target.Identity{ID: "different-target", HostKeyFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
				return state, err
			}
		}},
		{"unbound retirement receipt", func(c *Collector, _ fakeRunner) {
			read := c.StateInventory
			c.StateInventory = func(ctx context.Context) (target.ControlInventory, error) {
				state, err := read(ctx)
				state.Target = nil
				return state, err
			}
		}},
		{"generation alone", func(c *Collector, _ fakeRunner) {
			c.StateInventory = nil
			c.StateGeneration = func(context.Context) (uint64, error) { return 2, nil }
		}},
		{"unreadable units", func(c *Collector, _ fakeRunner) {
			c.FS.(fixtureFS).failures["/home/brine/.config/containers/systemd"] = os.ErrPermission
		}},
		{"unowned named container", func(_ *Collector, r fakeRunner) {
			r["podman --remote=false ps --all --format "+containerInventoryFormat] = "fixture"
		}},
		{"orphan container", func(_ *Collector, r fakeRunner) {
			r["podman --remote=false ps --all --format "+containerInventoryFormat] = "systemd-fixture fixture.service"
		}},
		{"foreign named container", func(_ *Collector, r fakeRunner) {
			r["podman --remote=false ps --all --format "+containerInventoryFormat] = "systemd-fixture foreign.service"
		}},
		{"container with renamed owner", func(_ *Collector, r fakeRunner) {
			r["podman --remote=false ps --all --format "+containerInventoryFormat] = "renamed fixture.service"
		}},
		{"unreadable containers", func(_ *Collector, r fakeRunner) {
			delete(r, "podman --remote=false ps --all --format "+containerInventoryFormat)
		}},
		{"malformed containers", func(_ *Collector, r fakeRunner) {
			r["podman --remote=false ps --all --format "+containerInventoryFormat] = "bad container row"
		}},
		{"unowned listener", func(_ *Collector, r fakeRunner) { r["ss -H -ltnpe"] = "LISTEN 0 128 127.0.0.1:20000 0.0.0.0:*" }},
		{"unowned UDP listener", func(_ *Collector, r fakeRunner) { r["ss -H -lunp"] = "UNCONN 0 0 *:20000 *:*" }},
		{"unreadable listeners", func(_ *Collector, r fakeRunner) { delete(r, "ss -H -ltnpe") }},
		{"unreadable UDP listeners", func(_ *Collector, r fakeRunner) { delete(r, "ss -H -lunp") }},
		{"malformed UDP listeners", func(_ *Collector, r fakeRunner) { r["ss -H -lunp"] = "unreadable row" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r, in := retainedFixture(t)
			tc.change(&c, r)
			observed, err := c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			in.Snapshot.Apps = observed.Apps
			in.Snapshot.Generation = observed.Generation
			in.Snapshot.UsedPorts = observed.UsedPorts
			in.Snapshot.PortOwners = observed.PortOwners
			p, err := plan.Build(in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != plan.Conflict {
				t.Fatalf("unsafe absence accepted: %+v", p)
			}
		})
	}
}

func TestRetiredAllocationCanBeOwnedByAnotherApp(t *testing.T) {
	c, _, _ := retainedFixture(t)
	s := target.Snapshot{Apps: target.Known([]target.App{{Name: "fixture", Image: unknown[target.Image](), AllocatedHostPort: unknown[target.Port](), QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{})}}), Generation: target.Known(uint64(2)), UsedPorts: target.Known([]target.Port{20000}), PortOwners: target.Known([]target.PortOwner{{Port: 20000, App: "another"}})}
	state, err := c.StateInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Identity = *state.Target
	c.absence(context.Background(), &s, appArtifacts{runner: true}, &state, target.Known([]target.Port{}))
	if (*s.Apps.Value)[0].AllocatedHostPort.Status != target.Absent {
		t.Fatal("retired allocation treated as a live reservation")
	}
}

func TestRetiredTCPAllocationDoesNotHideUDPRemnant(t *testing.T) {
	for _, udp := range []bool{false, true} {
		t.Run(map[bool]string{false: "TCP owned by another app", true: "same port also has unowned UDP"}[udp], func(t *testing.T) {
			c, r, in := retainedFixture(t)
			f := c.FS.(fixtureFS)
			unit := "# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nPublishPort=127.0.0.1:20000:8080\n"
			f.files["/home/brine/.config/containers/systemd/another.container"] = unit
			f.files["/proc/4242/cgroup"] = "0::/user.slice/user-1001.slice/user@1001.service/app.slice/another.service/runtime\n"
			f.files["/proc/4242/stat"] = "4242 (pasta (fixture)) S " + strings.Repeat("0 ", 18) + "123 0\n"
			f.links["/proc/4242/fd/6"] = "socket:[77777]"
			r["ss -H -ltnpe"] = `LISTEN 0 128 127.0.0.1:20000 0.0.0.0:* users:(("pasta",pid=4242,fd=6)) ino:77777 sk:1`
			if udp {
				r["ss -H -lunp"] = "UNCONN 0 0 *:20000 *:*"
			}
			in.Snapshot.Apps = target.Known([]target.App{
				{Name: "fixture", Image: unknown[target.Image](), AllocatedHostPort: unknown[target.Port](), QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{{ID: "fixture-id", Name: "brine.fixture.fixture-token.v1"}})},
				{Name: "another", Image: target.Known(target.Image{Digest: in.Image.Digest, Platform: in.Image.Platform}), AllocatedHostPort: target.Known(target.Port(20000)), QuadletUnits: target.Known([]target.Unit{{Name: "another.container", Hash: digest([]byte(unit))}}), Secrets: target.Known([]target.Secret{})},
			})
			c.RunnerUser = "brine"
			udpPorts := c.listeners(context.Background(), &in.Snapshot, "/home/brine", map[string]publication{"another": {Host: 20000, Container: 8080}})
			if in.Snapshot.PortOwners.Value == nil || len(*in.Snapshot.PortOwners.Value) != 1 || (*in.Snapshot.PortOwners.Value)[0].App != "another" {
				t.Fatalf("TCP ownership fixture failed: %+v", in.Snapshot.PortOwners)
			}
			state, err := c.StateInventory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			c.absence(context.Background(), &in.Snapshot, appArtifacts{runner: true}, &state, udpPorts)
			imageStatus := (*in.Snapshot.Apps.Value)[0].Image.Status
			p, err := plan.Build(in)
			if err != nil {
				t.Fatal(err)
			}
			removal, err := plan.BuildRemove(in)
			if err != nil {
				t.Fatal(err)
			}
			if udp {
				if imageStatus != target.Unknown || p.Kind != plan.Conflict || removal.Kind != plan.Conflict {
					t.Fatalf("TCP owner concealed UDP remnant: image=%s recreate=%s remove=%s", imageStatus, p.Kind, removal.Kind)
				}
			} else if p.Kind != plan.Create || p.HostPort != 20001 || removal.Kind != plan.NoOp {
				t.Fatalf("another app's TCP allocation should permit retirement: recreate=%s/%d remove=%s", p.Kind, p.HostPort, removal.Kind)
			}
		})
	}
}
