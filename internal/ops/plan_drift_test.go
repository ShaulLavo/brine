package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestPlanDriftEventRejectsUnsafePayloads(t *testing.T) {
	for _, raw := range []string{
		`{"paths":["snapshot.identity.private_key"],"changed":1,"truncated":false}`,
		`{"paths":["plan.diff.replica_sync.secret_key"],"changed":1,"truncated":false}`,
		`{"paths":["snapshot.identity.id"],"changed":1,"truncated":null}`,
		`{"paths":["snapshot.identity.id"],"changed":1,"truncated":false,"value":"private"}`,
		`{"paths":["snapshot.identity.id","snapshot.identity.id"],"changed":2,"truncated":false}`,
		`{"paths":[],"changed":0,"truncated":false}`,
	} {
		if ValidateEvent(Event{Kind: "plan_drift", Payload: json.RawMessage(raw)}) == nil {
			t.Fatal("unsafe diagnostic accepted", raw)
		}
	}
	valid := `{"paths":["snapshot.identity.id"],"changed":1,"truncated":false}`
	if err := ValidateEvent(Event{Kind: "plan_drift", Payload: json.RawMessage(valid)}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEvent(Event{Kind: "plan_drift", Payload: json.RawMessage(strings.Repeat(" ", MaxEventBytes) + valid)}); err == nil {
		t.Fatal("oversized event accepted")
	}
}

type driftJournal struct{ events []Event }

func (j *driftJournal) AppendEvent(_ context.Context, _ string, e Event) (uint64, error) {
	if err := ValidateEvent(e); err != nil {
		return 0, err
	}
	j.events = append(j.events, e)
	return uint64(len(j.events)), nil
}

func TestRecordPlanDriftObservedArrays(t *testing.T) {
	original := driftDecisionFixture(t)
	for _, tc := range []struct {
		field, path   string
		before, after json.RawMessage
	}{
		{"used_ports", "snapshot.used_ports.value[0]", driftObservedArray(t, target.Port(10000)), driftObservedArray(t, target.Port(10001))},
		{"apps", "snapshot.apps.value[0].name", driftObservedArray(t, target.App{Name: "before"}), driftObservedArray(t, target.App{Name: "after"})},
		{"port_owners", "snapshot.port_owners.value[0].process", driftObservedArray(t, target.PortOwner{Process: "before"}), driftObservedArray(t, target.PortOwner{Process: "after"})},
		{"persistent_data", "snapshot.persistent_data.value[0].fenced", driftObservedArray(t, target.PersistentDatabase{Fenced: false}), driftObservedArray(t, target.PersistentDatabase{Fenced: true})},
	} {
		t.Run(tc.field, func(t *testing.T) {
			testRecordedArrayDrift(t, original, tc.field, tc.path, tc.before, tc.after)
		})
	}
}

func testRecordedArrayDrift(t *testing.T, original []byte, field, path string, beforeValue, afterValue json.RawMessage) {
	t.Helper()
	before := driftPlanObservation(t, original, "before", field, beforeValue)
	after := driftPlanObservation(t, original, "after", field, afterValue)
	journal := &driftJournal{}
	if err := RecordPlanDrift(t.Context(), journal, "operation", before, after); err != nil {
		t.Fatal(err)
	}
	if len(journal.events) != 1 || journal.events[0].Kind != "plan_drift" {
		t.Fatal("missing diagnostic", journal.events)
	}
	var drift plan.DecisionDrift
	if err := json.Unmarshal(journal.events[0].Payload, &drift); err != nil || !drift.Valid() || !slices.Contains(drift.Paths, path) || !slices.Contains(drift.Paths, "snapshot.identity.id") {
		t.Fatalf("invalid observed-array event %+v %v", drift, err)
	}
}

func driftDecisionFixture(t *testing.T) []byte {
	t.Helper()
	root, err := os.OpenRoot("..")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	read := func(path string) []byte {
		raw, err := root.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	snapshot, err := target.Decode(read("target/testdata/ready-arm64.json"))
	if err != nil {
		t.Fatal(err)
	}
	operator, err := policy.Parse(bytes.ReplaceAll(read("policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(read("spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, operator)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(plan.Input{
		Desired: desired, Snapshot: snapshot,
		Image: plan.Image{Digest: strings.Split(string(desired.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}, ManifestDigest: target.Observation[string]{Status: target.Unknown}},
		State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p.DecisionInput()
}

func driftObservedArray[T any](t *testing.T, value T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(target.Known([]T{value}))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func driftPlanObservation(t *testing.T, original []byte, identity, field string, value json.RawMessage) plan.Plan {
	t.Helper()
	var document, snapshot map[string]json.RawMessage
	if err := json.Unmarshal(original, &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(document["snapshot"], &snapshot); err != nil {
		t.Fatal(err)
	}
	var targetIdentity target.Identity
	if err := json.Unmarshal(snapshot["identity"], &targetIdentity); err != nil {
		t.Fatal(err)
	}
	targetIdentity.ID = identity
	snapshot["identity"], _ = json.Marshal(targetIdentity)
	snapshot[field] = value
	document["snapshot"], _ = json.Marshal(snapshot)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	p, err := (plan.Plan{Hash: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}).WithDecisionInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecordPlanDriftRejectsPartialDocuments(t *testing.T) {
	original := driftDecisionFixture(t)
	for _, raw := range []string{
		`{"snapshot":{"identity":{"id":"before"},"used_ports":{"status":"known","value":[10000]}}}`,
		`{"snapshot":{"identity":{"id":"before"},"apps":{"status":"known","value":[{"name":"before"}]}}}`,
		`{"snapshot":{"identity":{"id":"before"},"port_owners":{"status":"known","value":[{"process":"before"}]}}}`,
		`{"snapshot":{"identity":{"id":"before"},"persistent_data":{"status":"known","value":[{"fenced":false}]}}}`,
	} {
		partial, err := (plan.Plan{Hash: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))}).WithDecisionInput([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		complete, err := (plan.Plan{Hash: fmt.Sprintf("sha256:%x", sha256.Sum256(original))}).WithDecisionInput(original)
		if err != nil {
			t.Fatal(err)
		}
		journal := &driftJournal{}
		if err := RecordPlanDrift(t.Context(), journal, "operation", partial, complete); err == nil || len(journal.events) != 0 {
			t.Fatalf("partial document recorded a diagnostic: %v %+v", err, journal.events)
		}
	}
}
