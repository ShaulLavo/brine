package plan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestManifestDigestBinding(t *testing.T) {
	in := fixture(t, "ready-arm64")
	unknown := build(t, in)
	in.Image.ManifestDigest = target.Known("sha256:" + strings.Repeat("b", 64))
	known := build(t, in)
	if unknown.Hash == known.Hash || unknown.ConfigHash == known.ConfigHash {
		t.Fatal("unknown and known manifest must have different fingerprints")
	}
	in.Image.ManifestDigest = target.Known("sha256:" + strings.Repeat("c", 64))
	changed := build(t, in)
	if known.Hash == changed.Hash || known.ConfigHash == changed.ConfigHash {
		t.Fatal("manifest change must change plan and configuration fingerprints")
	}
	in.Image.ManifestDigest = target.Known("sha256:" + strings.Repeat("b", 64))
	repeated := build(t, in)
	if !reflect.DeepEqual(known, repeated) {
		t.Fatal("equal observations with different pointers must produce identical plans")
	}
	*in.Image.ManifestDigest.Value = "sha256:" + strings.Repeat("d", 64)
	if *repeated.Image.ManifestDigest.Value != "sha256:"+strings.Repeat("b", 64) {
		t.Fatal("plan aliases caller manifest observation")
	}
}

func TestManifestObservationEncoding(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name        string
		observation target.Observation[string]
		want        string
	}{
		{"unknown", target.Observation[string]{Status: target.Unknown}, `{"status":"unknown"}`},
		{"known", target.Known(digest), `{"status":"known","value":"` + digest + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture(t, "ready-arm64")
			in.Image.ManifestDigest = tc.observation
			p := build(t, in)
			raw, err := json.Marshal(p.Image.ManifestDigest)
			if err != nil || string(raw) != tc.want {
				t.Fatalf("encoding = %s, %v", raw, err)
			}
			encoded, err := p.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			var decoded Plan
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			reencoded, err := decoded.CanonicalBytes()
			if err != nil || string(encoded) != string(reencoded) {
				t.Fatal("canonical round trip changed plan")
			}
		})
	}
}

func TestInvalidManifestObservations(t *testing.T) {
	for _, observation := range []target.Observation[string]{
		{}, {Status: target.Absent}, {Status: target.Unsupported},
		{Status: target.KnownStatus}, target.Known("invalid"),
		target.Known("sha256:" + strings.Repeat("B", 64)),
		{Status: target.Unknown, Value: new(string)},
	} {
		in := fixture(t, "ready-arm64")
		in.Image.ManifestDigest = observation
		if _, err := Build(in); err == nil {
			t.Fatalf("accepted invalid observation %#v", observation)
		}
		in = installed(t)
		in.State.Releases[0].Image.ManifestDigest = observation
		if _, err := Build(in); err == nil {
			t.Fatal("accepted invalid committed manifest observation")
		}
	}
}

func TestManifestOnlyUpdateAndNoOp(t *testing.T) {
	in := installed(t)
	in.Image.ManifestDigest = target.Known("sha256:" + strings.Repeat("b", 64))
	p := build(t, in)
	if p.Kind != Update || p.Diff == nil || p.Diff.Image == nil {
		t.Fatal("manifest observation change missing from update diff")
	}
	in.State.Releases[0].Image.ManifestDigest = target.Known(*in.Image.ManifestDigest.Value)
	p = build(t, in)
	if p.Kind != NoOp {
		t.Fatalf("equal manifest values should be no-op, got %s", p.Kind)
	}
}
