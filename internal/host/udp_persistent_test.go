//go:build linux

package host

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestPersistentUDPCommitKeepsWriterSchemaAvailable(t *testing.T) {
	database, dataDesired, observer := persistentHostFixture(t)
	fact := collectPersistent(t, observer, dataDesired)
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
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
	desired.Runtime, desired.Backup, desired.Databases = dataDesired.Runtime, dataDesired.Backup, dataDesired.Databases
	desired.PersistentRoots, desired.BackupDestinations = dataDesired.PersistentRoots, dataDesired.BackupDestinations
	desired.SchemaCompatibility, desired.BackupRetention = dataDesired.SchemaCompatibility, dataDesired.BackupRetention
	desired.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}}
	raw, err = os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	fact.Credentials = target.Known(data.CredentialEvidence{BindingID: fact.Database.ReplicaBindingID, EpochID: "55555555555555555555555555555555", Destination: "primary", Reference: "primary", Version: 1, PolicyHash: desired.PolicyHash, ReceivedAt: fact.Schema.ObservedAt.Add(-time.Hour)})
	facts := target.Known([]target.PersistentDatabase{fact})
	snapshot.PersistentData = &facts
	snapshot.LocalAddresses = target.Known([]string{"192.0.2.10"})
	state, err := database.LoadBrineState(t.Context(), snapshot.Identity, 0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(plan.Input{Desired: desired, Snapshot: snapshot, State: state, Image: plan.Image{Digest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: target.Known("sha256:" + strings.Repeat("b", 64)), Platform: target.Platform{OS: "linux", Arch: "arm64"}}})
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal(p.Conflicts, err)
	}
	if _, err := database.SavePlan(t.Context(), p, desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "udp-test", "persistent")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PrepareUDP(t.Context(), op.ID, p); err != nil {
		t.Fatal(err)
	}
	unit, err := quadlet.Render(desired, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	release := ops.Release{ID: op.ID, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
	if err := database.CommitRelease(t.Context(), p.App, release); err != nil {
		t.Fatal(err)
	}
	schema, err := database.ReadWriterSchema(t.Context(), fact.Database.IncarnationID)
	if err != nil || schema.ReleaseID != op.ID || len(schema.Bindings) != 1 || schema.Bindings[0] != fact.Database {
		t.Fatal("UDP blocked normal writer declarations", err)
	}
	if err := database.CheckArchiveUDP(t.Context(), p.App); err == nil {
		t.Fatal("retirement admitted live UDP")
	}
	if _, err := database.ReadWriterSchema(t.Context(), fact.Database.IncarnationID); err != nil {
		t.Fatal("retirement refusal fenced ordinary writer", err)
	}
}
