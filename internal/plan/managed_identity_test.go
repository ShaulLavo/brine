package plan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestRecordedEngineIdentityRemainsReadableOutsideSelectedProfile(t *testing.T) {
	in := managedInitializedFixture(t, false)
	runtime := in.Managed.Runtime
	runtime.Image = "registry.example/retained-engine@sha256:" + strings.Repeat("1", 64)
	runtime.ManifestDigest = "sha256:" + strings.Repeat("2", 64)
	in.Managed.EngineImage.Digest = "sha256:" + strings.Repeat("1", 64)
	in.Managed.EngineImage.ManifestDigest = target.Known(runtime.ManifestDigest)
	in.Desired.SpacetimeDB.Image = spec.ImageReference(runtime.Image)
	raw, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var decoded data.EngineRuntime
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal("recorded runtime selected today's artifact", err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(raw, canonical) {
		t.Fatal("recorded runtime bytes changed", err)
	}
	planned, err := Build(in)
	if err != nil || planned.Managed == nil || len(planned.Conflicts) != 0 {
		t.Fatal("retained runtime cannot serve an unchanged deployment", err, planned.Conflicts)
	}
	raw, err = json.Marshal(planned.Managed)
	if err != nil {
		t.Fatal(err)
	}
	var retained ManagedServicePlan
	if err = json.Unmarshal(raw, &retained); err != nil || retained.AdmitsNewEngine(runtime.Image) {
		t.Fatal("historical decoding granted a new allocation", err)
	}
}

func TestManagedPreparationRequiresSelectedImageAndPlatform(t *testing.T) {
	for _, mutation := range []string{"image", "manifest", "platform"} {
		t.Run(mutation, func(t *testing.T) { testUnselectedManagedPreparation(t, mutation) })
	}
}

func testUnselectedManagedPreparation(t *testing.T, mutation string) {
	t.Helper()
	in, proposal, root := managedPreparationFixture(t, false)
	changeManagedProfile(&in, &proposal, mutation)
	if !proposal.Valid() {
		t.Fatal("syntactically valid recorded plan rejected")
	}
	if _, err := BuildManagedPreparation(in, proposal, root); err == nil {
		t.Fatal("unselected fresh allocation admitted")
	}
}

func changeManagedProfile(in *Input, proposal *ManagedServicePlan, mutation string) {
	switch mutation {
	case "image":
		in.Desired.SpacetimeDB.Image = spec.ImageReference("registry.example/engine@sha256:" + strings.Repeat("1", 64))
		proposal.EngineImage.Digest = "sha256:" + strings.Repeat("1", 64)
	case "manifest":
		proposal.EngineImage.ManifestDigest = target.Known("sha256:" + strings.Repeat("2", 64))
	case "platform":
		proposal.EngineImage.ManifestDigest = target.Known(data.SpacetimeDBAMD64Manifest)
	}
	in.Managed.EngineImage = proposal.EngineImage
}

func TestRecordedEngineIdentityStillRejectsMalformedPinsAndForeignVersion(t *testing.T) {
	runtime := *managedInitializedFixture(t, false).Managed.Runtime
	for _, mutate := range []func(*data.EngineRuntime){
		func(r *data.EngineRuntime) { r.Image = "registry.example/engine:latest" },
		func(r *data.EngineRuntime) { r.ManifestDigest = "sha256:bad" },
		func(r *data.EngineRuntime) { r.ImageID = "bad" },
		func(r *data.EngineRuntime) { r.Version = "2.12.0" },
	} {
		changed := runtime.Clone()
		mutate(&changed)
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		var decoded data.EngineRuntime
		if json.Unmarshal(raw, &decoded) == nil {
			t.Fatal("invalid recorded identity accepted")
		}
	}
}
