package plan

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func checkManagedPlanInputDetachment(t *testing.T, frontend bool) {
	t.Helper()
	in, _ := managedPlanInput(t, frontend)
	in.Image.ManifestDigest = target.Known("sha256:" + strings.Repeat("c", 64))
	p, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	*in.Snapshot.Generation.Value += 7
	*in.Image.ManifestDigest.Value = "sha256:" + strings.Repeat("d", 64)
	in.Desired.SpacetimeDB.Module.Accepts[0] = "changed"
	in.Desired.SpacetimeDB.Resources.MemoryMB++
	after, err := p.CanonicalBytes()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("caller input mutation changed managed plan bytes under its existing hash", err)
	}
}

func TestManagedPlansDetachCallerInputs(t *testing.T) {
	t.Run("engine only", func(t *testing.T) { checkManagedPlanInputDetachment(t, false) })
	t.Run("frontend", func(t *testing.T) { checkManagedPlanInputDetachment(t, true) })
}

func TestManagedFrontendRefusesMalformedImage(t *testing.T) {
	cases := []func(*Input){
		func(in *Input) { in.Image.ManifestDigest = target.Observation[string]{} },
		func(in *Input) { in.Image.ManifestDigest = target.Known("sha256:bad") },
		func(in *Input) { in.Image.Digest = "sha256:bad" },
		func(in *Input) { in.Image.Digest = "sha256:" + strings.Repeat("b", 64) },
	}
	for _, mutate := range cases {
		in, _ := managedPlanInput(t, true)
		mutate(&in)
		if _, err := Build(in); err == nil {
			t.Fatal("managed preview accepted malformed or unrelated frontend image evidence")
		}
	}
	in, _ := managedPlanInput(t, true)
	in.Image.Platform.Arch = "amd64"
	p, err := Build(in)
	if err != nil || len(p.Changes) != 0 || !slices.ContainsFunc(p.Conflicts, func(c Diagnostic) bool { return c.Code == ImagePlatform }) {
		t.Fatal("managed frontend platform mismatch did not conflict", err)
	}
}
