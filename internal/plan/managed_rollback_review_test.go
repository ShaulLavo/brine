package plan

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
	"slices"
	"strings"
	"testing"
)

func TestReviewClosedSchemaProbe(t *testing.T) {
	original := build(t, managedInitializedFixture(t, true)).DecisionInput()
	cases := map[string][]byte{
		"duplicate":            []byte(strings.Replace(string(original), `"route_revision":`, `"route_revision":999,"route_revision":`, 1)),
		"alias":                []byte(strings.Replace(string(original), `"logging_root_sha256":`, `"LOGGING_ROOT_SHA256":`, 1)),
		"required_scalar_null": []byte(strings.Replace(string(original), `"route_revision":0`, `"route_revision":null`, 1)),
		"required_object_null": []byte(strings.Replace(string(original), `"native_state":{"status":"known","value":"running"}`, `"native_state":null`, 1)),
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(original, &d); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(d["snapshot"], &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["managed"] = []byte(`null`)
	d["snapshot"], _ = json.Marshal(snapshot)
	cases["managed_null"], _ = json.Marshal(d)
	for name, altered := range cases {
		t.Run(name, func(t *testing.T) { reviewRejectDocument(t, original, altered) })
	}
}

func reviewRejectDocument(t *testing.T, original, altered []byte) {
	t.Helper()
	if string(original) == string(altered) {
		t.Fatal("probe did not mutate input")
	}
	drift, err := CompareDecisionInputs(original, altered)
	if err == nil {
		t.Errorf("malformed input accepted: drift=%+v valid=%t", drift, drift.Valid())
	}
}

func TestReviewLifecycleSelection(t *testing.T) {
	in := managedInitializedFixture(t, true)
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	v1 := target.Secret{Name: "brine.hello.token.v1", ID: "fixture-token-001"}
	v2 := target.Secret{Name: "brine.hello.token.v2", ID: "fixture-token-002"}
	observed := target.App{Name: "hello", Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{v1}), UDPPublication: target.Observation[target.UDPPublication]{Status: target.Absent}}
	in.Snapshot.Apps = target.Known([]target.App{observed})
	p := build(t, in)
	unit := target.Unit{Name: "hello.container", Hash: "sha256:" + strings.Repeat("a", 64)}
	current := CurrentRelease{App: p.App, ID: "retained-v1", Desired: in.Desired, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{unit}, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: "sha256:" + strings.Repeat("b", 64)}, Managed: p.Managed}
	in.State.Releases = []CurrentRelease{current}
	observed.Image = target.Known(target.Image{Digest: p.Image.Digest, Platform: p.Image.Platform})
	observed.AllocatedHostPort = target.Known(p.HostPort)
	observed.QuadletUnits = target.Known([]target.Unit{unit})
	observed.Secrets = target.Known([]target.Secret{v1, v2})
	in.Snapshot.Apps = target.Known([]target.App{observed})
	for _, action := range []ChangeKind{StartApp, StopApp, RestartApp} {
		reviewLifecycleBinding(t, in, current, action)
	}
}

func reviewLifecycleBinding(t *testing.T, in Input, current CurrentRelease, action ChangeKind) {
	t.Helper()
	p, err := BuildLifecycle(in, action)
	if err != nil || p.Kind == Conflict || p.Managed == nil {
		t.Fatal("fixture refused", err, p.Conflicts)
	}
	if !p.SecretExact || !slices.Equal(p.Secrets, current.Secrets) || !slices.Equal(p.Managed.Frontend.Secrets, current.Secrets) {
		t.Errorf("%s selects %+v instead of current %+v", action, p.Secrets, current.Secrets)
	}
	input, _, err := RebuildInput(in, p, []string{"lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := BuildLifecycle(input, action)
	if err != nil || fresh.Hash != p.Hash || !fresh.SecretExact {
		t.Fatal("persisted selection differs on lifecycle rebuild", err)
	}
	forward := build(t, in)
	if forward.Secrets[0].VersionName != "brine.hello.token.v2" {
		t.Fatal("ordinary deployment lost latest selection", forward.Secrets)
	}

}

func TestDecisionReaderRejectsMalformedOrdinaryAndManagedDocuments(t *testing.T) {
	plain := build(t, fixture(t, "ready-arm64")).DecisionInput()
	managed := build(t, managedInitializedFixture(t, true)).DecisionInput()
	for _, original := range [][]byte{plain, managed} {
		testDecisionDocumentFields(t, original)
	}
}

func testDecisionDocumentFields(t *testing.T, original []byte) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"desired", "snapshot", "brine_state", "plan"} {
		testDecisionRequiredField(t, original, fields, field)
	}
	for _, altered := range [][]byte{
		[]byte(strings.Replace(string(original), `"desired":`, `"desired":null,"desired":`, 1)),
		[]byte(strings.Replace(string(original), `"desired":`, `"DESIRED":`, 1)),
		append(append([]byte{}, original...), []byte(` {}`)...),
	} {
		reviewRejectDocument(t, original, altered)
	}
}

func testDecisionRequiredField(t *testing.T, original []byte, fields map[string]json.RawMessage, field string) {
	t.Helper()
	before := fields[field]
	delete(fields, field)
	missing, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	reviewRejectDocument(t, original, missing)
	fields[field] = []byte(`null`)
	null, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	reviewRejectDocument(t, original, null)
	fields[field] = before
}
