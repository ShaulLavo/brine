package plan_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/planfile"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestCurrentPlanIDDoesNotChangeOfflineBytes(t *testing.T) {
	read := func(path string) []byte {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	snapshot, err := target.Decode(read("../target/testdata/one-app.json"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, rules)
	if err != nil {
		t.Fatal(err)
	}
	observed := (*snapshot.Apps.Value)[0]
	image := plan.Image{Digest: observed.Image.Value.Digest, Platform: observed.Image.Value.Platform, ManifestDigest: target.Observation[string]{Status: target.Unknown}}
	current := plan.CurrentRelease{App: "hello", ID: "current", Desired: desired, Image: image, HostPort: *observed.AllocatedHostPort.Value, Secrets: []plan.SecretBinding{}, Units: *observed.QuadletUnits.Value, CaddyFile: snapshot.CaddyConfig.Value.Files[0], CaddyGeneration: snapshot.CaddyConfig.Value.Generation}
	in := plan.Input{Desired: desired, Snapshot: snapshot, Image: image, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{current}}}
	metadata := planfile.Metadata{CreatedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), ToolVersion: "fixture"}
	before, err := planfile.New(in, metadata)
	if err != nil {
		t.Fatal(err)
	}
	in.State.Releases[0].PlanID = "sha256:" + strings.Repeat("e", 64)
	after, err := planfile.New(in, metadata)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := json.Marshal(after)
	if err != nil || !bytes.Equal(beforeBytes, afterBytes) || before.Hash() != after.Hash() || before.Filename() != after.Filename() {
		t.Fatalf("runtime metadata changed offline bytes or fingerprint: %v", err)
	}
	decoded, err := planfile.Decode(afterBytes)
	if err != nil || decoded.Hash() != before.Hash() || decoded.Applyable() {
		t.Fatalf("offline round trip changed: %v", err)
	}
}
