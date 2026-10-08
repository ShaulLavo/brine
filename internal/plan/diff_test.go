package plan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestUpdateComparisonAndEnvironmentRedaction(t *testing.T) {
	in := installed(t)
	in.State.Releases[0].Desired.Environment = []policy.Environment{{Name: "REMOVE", Value: "SYNTHETIC_OLD_PRIVATE"}, {Name: "CHANGE", Value: "SYNTHETIC_OLD_PRIVATE"}, {Name: "KEEP", Value: "same"}}
	in.Desired.Environment = []policy.Environment{{Name: "ADD", Value: "SYNTHETIC_NEW_PRIVATE"}, {Name: "CHANGE", Value: "SYNTHETIC_NEW_PRIVATE"}, {Name: "KEEP", Value: "same"}}
	in.Desired.Domains = []spec.Domain{"new.example.com"}
	in.Desired.ContainerPort = 4000
	in.Desired.Health.Path = "/ready"
	in.Desired.Resources.MemoryMB = 256
	in.Desired.Image = spec.ImageReference(strings.ReplaceAll(string(in.Desired.Image), strings.Repeat("a", 64), strings.Repeat("b", 64)))
	in.Image.Digest = "sha256:" + strings.Repeat("b", 64)
	p := build(t, in)
	if p.Kind != Update || p.Diff == nil {
		t.Fatal("missing update diff")
	}
	if p.Diff.Image.From.Digest == p.Diff.Image.To.Digest || *p.Diff.ContainerPort.From != 3000 || *p.Diff.ContainerPort.To != 4000 {
		t.Fatal("lost previous values")
	}
	if strings.Join(p.Diff.Environment.Added, ",") != "ADD" || strings.Join(p.Diff.Environment.Removed, ",") != "REMOVE" || strings.Join(p.Diff.Environment.Changed, ",") != "CHANGE" {
		t.Fatal(p.Diff.Environment)
	}
	if len(p.Diff.Domains.Added) != 1 || len(p.Diff.Domains.Removed) != 1 {
		t.Fatal(p.Diff.Domains)
	}
	raw, err := p.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SYNTHETIC_")) {
		t.Fatal("plan retains environment values")
	}
	for _, c := range p.Changes {
		if c.Quadlet != nil && len(c.Quadlet.Desired.Environment) != 0 {
			t.Fatal("quadlet retains literal environment")
		}
	}
	before, _ := json.Marshal(p)
	p.Diff.Environment.Changed[0] = "EDITED"
	after, _ := json.Marshal(p)
	if bytes.Equal(before, after) {
		t.Fatal("comparison absent from serialization")
	}
}

func TestDiffSecretRotationAndRemoval(t *testing.T) {
	old := CurrentRelease{Desired: policy.Desired{}, Secrets: []SecretBinding{{Environment: "ROTATE", Reference: "old-reference", VersionName: "brine-hello-old-reference-v1"}, {Environment: "REMOVE", Reference: "removed-reference", VersionName: "brine-hello-removed-reference-v1"}}}
	next := []SecretBinding{{Environment: "ADD", Reference: "added-reference", VersionName: "brine-hello-added-reference-v1"}, {Environment: "ROTATE", Reference: "new-reference", VersionName: "brine-hello-new-reference-v2"}}
	d := configurationDiff(old.Desired, old.Image, old.HostPort, next, &old)
	if len(d.Secrets) != 3 || d.Secrets[0].Environment != "ADD" || d.Secrets[0].From != nil || d.Secrets[1].Environment != "REMOVE" || d.Secrets[1].To != nil || d.Secrets[2].From.Reference != "old-reference" || d.Secrets[2].To.VersionName != "brine-hello-new-reference-v2" {
		t.Fatal(d.Secrets)
	}
}

func TestDiffOwnsPreviousValues(t *testing.T) {
	in := installed(t)
	in.Desired.ContainerPort++
	in.Desired.Health.Path = "/ready"
	in.Desired.Resources.MemoryMB++
	p := build(t, in)
	*p.Diff.ContainerPort.From = 5000
	p.Diff.Health.From.Path = "/mutated"
	p.Diff.Resources.From.MemoryMB = 5000
	if in.State.Releases[0].Desired.ContainerPort == 5000 || in.State.Releases[0].Desired.Health.Path == "/mutated" || in.State.Releases[0].Desired.Resources.MemoryMB == 5000 {
		t.Fatal("diff aliased caller state")
	}
}

func TestDiffIsHashMaterial(t *testing.T) {
	in := installed(t)
	in.Desired.Environment = []policy.Environment{{Name: "KEY", Value: "SYNTHETIC_PRIVATE_VALUE"}}
	p := build(t, in)
	desired, err := in.Desired.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Encode(in.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, err := canonicalState(in.State)
	if err != nil {
		t.Fatal(err)
	}
	original := p.Hash
	p.Hash = ""
	q, err := finish(p, desired, snapshot, state)
	if err != nil || q.Hash != original {
		t.Fatal("cannot reconstruct hash", err)
	}
	p.Diff.Environment.Added[0] = "OTHER"
	q, err = finish(p, desired, snapshot, state)
	if err != nil || q.Hash == original {
		t.Fatal("comparison is not hashed", err)
	}
	in.Desired.Environment[0].Value += "different"
	r := build(t, in)
	if r.Hash == original {
		t.Fatal("redaction lost hashed value sensitivity")
	}
}

func TestNoOpAndConflictHaveNoDiff(t *testing.T) {
	for _, name := range []string{"no-op", "conflict"} {
		p := build(t, goldenInputs(t)[name])
		if p.Diff != nil {
			t.Fatal(name, "has executable comparison")
		}
	}
}
