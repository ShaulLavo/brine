package plan

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestDecisionDriftReportsInputsWithoutValues(t *testing.T) {
	in := fixture(t, "ready-arm64")
	before := build(t, in)
	in.Snapshot.Identity.ID = "private-identity"
	in.Desired.Environment = []policy.Environment{{Name: "PRIVATE_KEY", Value: "/private/root/secret-value"}}
	after := build(t, in)
	drift, err := CompareDecisionInputs(before.DecisionInput(), after.DecisionInput())
	if err != nil || !slices.Contains(drift.Paths, "desired.environment") || !slices.Contains(drift.Paths, "snapshot.identity.id") || drift.Changed < 2 || drift.Truncated {
		t.Fatalf("unexpected drift %+v %v", drift, err)
	}
	raw, _ := json.Marshal(drift)
	for _, secret := range []string{"PRIVATE_KEY", "private-identity", "/private/root", "secret-value"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private value in diagnostic")
		}
	}
}

func TestDecisionDriftBoundsAndValidatesPaths(t *testing.T) {
	in := fixture(t, "ready-arm64")
	for i := 0; i < 100; i++ {
		in.Desired.Environment = append(in.Desired.Environment, policy.Environment{Name: strings.Repeat("A", i+1), Value: "before"})
	}
	before := build(t, in)
	for i := range in.Desired.Environment {
		in.Desired.Environment[i].Value = "after"
	}
	after := build(t, in)
	drift, err := CompareDecisionInputs(before.DecisionInput(), after.DecisionInput())
	if err != nil || len(drift.Paths) != MaxDecisionPaths || drift.Changed < 100 || !drift.Truncated {
		t.Fatalf("unbounded drift %+v %v", drift, err)
	}
	raw, _ := json.Marshal(drift)
	if len(raw) > 4096 {
		t.Fatal("diagnostic exceeds event limit")
	}
	if !drift.Valid() {
		t.Fatal("generated drift invalid")
	}
	for _, path := range []string{"desired.environment.PRIVATE_KEY", "snapshot./private/root", "snapshot.identity.secret_value", "desired.environment[0].value\n"} {
		invalid := DecisionDrift{Paths: []string{path}, Changed: 1}
		if invalid.Valid() {
			t.Fatalf("unsafe path accepted %q", path)
		}
	}
}

func TestDecisionDriftUsesBoundedAdmissionFacts(t *testing.T) {
	in := persistentReady(t)
	before := build(t, in)
	in.Snapshot.FreeDiskBytes = target.Known(in.Desired.MinimumFreeDiskBytes * 3)
	fact := &(*in.Snapshot.PersistentData.Value)[0]
	fact.Root.FreeBytes++
	fact.Root.FreeInodes++
	fact.Schema.ObservedAt = fact.Schema.ObservedAt.Add(1)
	after := build(t, in)
	drift, err := CompareDecisionInputs(before.DecisionInput(), after.DecisionInput())
	if err != nil || drift.Changed != 0 || len(drift.Paths) != 0 {
		t.Fatalf("reporting facts became decision drift %+v %v", drift, err)
	}
}

func TestDecisionDriftRedactsDynamicMapKeys(t *testing.T) {
	before, err := decodeDecisionInput(build(t, fixture(t, "ready-arm64")).DecisionInput())
	if err != nil {
		t.Fatal(err)
	}
	before.Plan.Diff = &ConfigurationDiff{ReplicaSync: map[string]ValueChange[time.Duration]{"/private/key": {}}}
	a, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	before.Plan.Diff.ReplicaSync = map[string]ValueChange[time.Duration]{"secret-key": {}}
	b, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	drift, err := CompareDecisionInputs(a, b)
	if err != nil || !slices.Equal(drift.Paths, []string{"plan.diff.replica_sync"}) || !drift.Valid() {
		t.Fatalf("map key exposed %+v %v", drift, err)
	}
}

func TestDecisionDriftObservedArrayPaths(t *testing.T) {
	before, err := decodeDecisionInput(build(t, persistentReady(t)).DecisionInput())
	if err != nil {
		t.Fatal(err)
	}
	before.Snapshot.UsedPorts = target.Known([]target.Port{10000})
	before.Snapshot.Apps = target.Known([]decisionApp{{Name: "before"}})
	before.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 10000, Process: "before"}})
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		change func(*decisionDocument)
	}{
		{"snapshot.used_ports.value[0]", func(d *decisionDocument) { (*d.Snapshot.UsedPorts.Value)[0]++ }},
		{"snapshot.apps.value[0].name", func(d *decisionDocument) { (*d.Snapshot.Apps.Value)[0].Name = "after" }},
		{"snapshot.port_owners.value[0].process", func(d *decisionDocument) { (*d.Snapshot.PortOwners.Value)[0].Process = "after" }},
		{"snapshot.persistent_data.value[0].fenced", func(d *decisionDocument) {
			(*d.Snapshot.PersistentData.Value)[0].Fenced = !(*d.Snapshot.PersistentData.Value)[0].Fenced
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			after, err := decodeDecisionInput(raw)
			if err != nil {
				t.Fatal(err)
			}
			after.Snapshot.Identity.ID = "private-changed-identity"
			tc.change(&after)
			fresh, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
			drift, err := CompareDecisionInputs(raw, fresh)
			if err != nil || !drift.Valid() || !slices.Contains(drift.Paths, tc.path) || !slices.Contains(drift.Paths, "snapshot.identity.id") {
				t.Fatalf("observed-array drift invalid %+v %v", drift, err)
			}
		})
	}
}

func TestDecisionDriftOmitsInvalidPathsWithoutLosingValidChanges(t *testing.T) {
	drift := DecisionDrift{Paths: []string{}}
	drift.addPath("desired.environment.PRIVATE_KEY")
	drift.addPath("snapshot.identity.id")
	if !drift.Valid() || !slices.Equal(drift.Paths, []string{"snapshot.identity.id"}) || drift.Changed != 2 || !drift.Truncated {
		t.Fatalf("invalid path poisoned valid changes %+v", drift)
	}
}

func TestManagedDecisionDriftDecodesRealDeploymentDocuments(t *testing.T) {
	in := managedInitializedFixture(t, true)
	before := build(t, in)
	in.Managed.LoggingRootSHA256 = strings.Repeat("e", 64)
	in.Snapshot.Identity.ID = "private-managed-identity"
	after := build(t, in)
	drift, err := CompareDecisionInputs(before.DecisionInput(), after.DecisionInput())
	if err != nil || !drift.Valid() || !slices.Contains(drift.Paths, "snapshot.managed.logging_root_sha256") || !slices.Contains(drift.Paths, "snapshot.snapshot.identity.id") {
		t.Fatalf("managed decision document drift %+v: %v", drift, err)
	}
	raw, err := json.Marshal(drift)
	if err != nil || strings.Contains(string(raw), "private-managed-identity") || strings.Contains(string(raw), strings.Repeat("e", 64)) {
		t.Fatal("managed drift leaked values", err)
	}
}

func TestManagedDecisionDriftKeepsClosedSchemas(t *testing.T) {
	in := managedInitializedFixture(t, true)
	original := build(t, in).DecisionInput()
	for _, change := range []struct{ old, next string }{
		{`"logging_root_sha256":`, `"unknown_managed":true,"logging_root_sha256":`},
		{`"sufficient_disk":`, `"unknown_snapshot":true,"sufficient_disk":`},
		{`"brine_state":`, `"unknown_document":true,"brine_state":`},
		{`"managed":{"route_revision":`, `"managed":{"unknown_fact":true,"route_revision":`},
	} {
		altered := strings.Replace(string(original), change.old, change.next, 1)
		if altered == string(original) {
			t.Fatal("fixture field missing", change.old)
		}
		if _, err := CompareDecisionInputs(original, []byte(altered)); err == nil {
			t.Fatal("unknown managed decision field accepted", change.next)
		}
	}
	if _, err := CompareDecisionInputs(original, append(slices.Clone(original), []byte(` {}`)...)); err == nil {
		t.Fatal("trailing decision document accepted")
	}
}

func TestManagedDecisionDriftDecodesLifecycleAndPreparationDocuments(t *testing.T) {
	in := managedInitializedFixture(t, true)
	created := build(t, in)
	in.State.Releases = []CurrentRelease{{App: created.App, ID: "current", Desired: in.Desired, Image: created.Image, HostPort: created.HostPort, Secrets: created.Secrets, Units: []target.Unit{}, CaddyFile: target.CaddyFile{Name: created.App + ".caddy", Hash: "sha256:" + strings.Repeat("a", 64)}, Managed: created.Managed}}
	lifecycle, err := BuildLifecycle(in, StartApp)
	if err != nil {
		t.Fatal(err)
	}
	preparation, proposed, root := managedPreparationFixture(t, true)
	prepared, err := BuildManagedPreparation(preparation, proposed, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Plan{created, lifecycle, prepared} {
		drift, err := CompareDecisionInputs(p.DecisionInput(), p.DecisionInput())
		if err != nil || !drift.Valid() || drift.Changed != 0 {
			t.Fatal("real managed decision cannot decode", err, drift)
		}
	}
	plain := build(t, fixture(t, "ready-arm64"))
	drift, err := CompareDecisionInputs(plain.DecisionInput(), created.DecisionInput())
	if err != nil || !drift.Valid() || !slices.Contains(drift.Paths, "snapshot") {
		t.Fatal("different decision domains cannot compare", err, drift)
	}
}
