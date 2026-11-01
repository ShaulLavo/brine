//go:build linux

package inventory

import (
	"bytes"
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestUDPPreparedUnpublishedCollectorRebuildUsesOnlyOwnFacts(t *testing.T) {
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	fs := secretOnlyFixture()
	runner := fakeRunner{"uname -m": "aarch64", "ss -H -ltnpe": "", "ss -H -lunpe": "", "ip -j addr show": `[{"addr_info":[{"local":"192.0.2.10"}]}]`, "podman --remote=false ps --all --format " + containerInventoryFormat: "", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": ""}
	collector := Collector{FS: fs, Runner: runner, IdentityKey: []byte("fixture"), StateInventory: database.InventoryState}
	baseline, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apps = baseline.Apps
	snapshot.UDPSockets = baseline.UDPSockets
	snapshot.LocalAddresses = baseline.LocalAddresses
	raw, err = os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(bytes.ReplaceAll(raw, []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(raw, []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	desired.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4435}}}
	in := plan.Input{Desired: desired, Snapshot: snapshot, Image: plan.Image{Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ManifestDigest: target.Known("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), Platform: target.Platform{OS: "linux", Arch: "arm64"}}, State: plan.BrineState{Target: snapshot.Identity, Releases: []plan.CurrentRelease{}, UDP: &plan.UDPState{Incarnations: []plan.AppIncarnation{}, Reservations: []plan.UDPReservation{}}}}
	p, err := plan.Build(in)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal(p.Conflicts, err)
	}
	if _, err := database.SavePlan(t.Context(), p, desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "udp-test", "prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PrepareUDP(t.Context(), op.ID, p); err != nil {
		t.Fatal(err)
	}
	prepared, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Apps.Value == nil || len(*prepared.Apps.Value) != 1 || (*prepared.Apps.Value)[0].RuntimeAbsent() {
		t.Fatal("live unpublished claim became reusable absence")
	}
	rebuilt, err := collector.RebuildUDP(t.Context(), p, []string{op.ID})
	if err != nil {
		t.Fatal(err)
	}
	before, err := target.Encode(baseline)
	if err != nil {
		t.Fatal(err)
	}
	after, err := target.Encode(rebuilt)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("own collector facts did not rebuild exactly", err)
	}
	runner["ss -H -lunpe"] = "UNCONN 0 0 192.0.2.10:4433 *:*"
	rebuilt, err = collector.RebuildUDP(t.Context(), p, []string{op.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.UDPSockets.Value == nil || len(*rebuilt.UDPSockets.Value) != 1 {
		t.Fatal("real socket hidden")
	}
	other, err := collector.RebuildUDP(t.Context(), p, []string{"foreign"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Apps.Value == nil || len(*other.Apps.Value) != 1 || (*other.Apps.Value)[0].RuntimeAbsent() {
		t.Fatal("foreign collector claim hidden")
	}
}
