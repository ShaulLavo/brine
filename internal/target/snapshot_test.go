package target

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFixtures(t *testing.T) {
	for _, name := range []string{"fresh-arm64", "one-app", "port-conflict", "unsupported-ubuntu"} {
		t.Run(name, func(t *testing.T) {
			b := fixture(t, name)
			s, err := Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			err = s.Validate()
			if name == "unsupported-ubuntu" {
				var unsupported *UnsupportedError
				if !errors.As(err, &unsupported) || unsupported.Code() != "unsupported" {
					t.Fatalf("expected unsupported, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			canonical, err := Encode(s)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b, canonical) {
				t.Fatalf("fixture is not canonical:\n%s", canonical)
			}
			again, err := Decode(canonical)
			if err != nil {
				t.Fatal(err)
			}
			canonical2, err := Encode(again)
			if err != nil || !bytes.Equal(canonical, canonical2) {
				t.Fatalf("round trip failed: %v", err)
			}
		})
	}
}

func TestCanonicalOrderingAndNoMutation(t *testing.T) {
	s, err := Decode(fixture(t, "one-app"))
	if err != nil {
		t.Fatal(err)
	}
	app := (*s.Apps.Value)[0]
	app.QuadletUnits = Known(append([]Unit(nil), (*app.QuadletUnits.Value)...))
	app.Secrets = Known(append([]Secret(nil), (*app.Secrets.Value)...))
	app.Name = "another"
	app.AllocatedHostPort = Known(Port(20001))
	*s.Apps.Value = append(*s.Apps.Value, app)
	*s.UsedPorts.Value = []Port{20001, 20000, 443}
	want, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	apps := *s.Apps.Value
	apps[0], apps[1] = apps[1], apps[0]
	for i := range apps {
		units := *apps[i].QuadletUnits.Value
		units[0], units[1] = units[1], units[0]
		secrets := *apps[i].Secrets.Value
		secrets[0], secrets[1] = secrets[1], secrets[0]
	}
	*s.UsedPorts.Value = []Port{443, 20000, 20001}
	before, _ := json.Marshal(s)
	got, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(want, got) {
		t.Fatalf("ordering changes bytes:\n%s\n%s", want, got)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("Encode mutated input")
	}
}

func TestRejectJSON(t *testing.T) {
	base := string(fixture(t, "one-app"))
	tests := map[string]string{
		"unknown root":           strings.Replace(base, `"schema_version":1`, `"extra":true,"schema_version":1`, 1),
		"unknown nested":         strings.Replace(base, `"id":"fixture-target"`, `"extra":true,"id":"fixture-target"`, 1),
		"wrong version":          strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1),
		"missing version":        strings.Replace(base, `"schema_version":1,`, ``, 1),
		"duplicate":              strings.Replace(base, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"case alias":             strings.Replace(base, `"schema_version":1`, `"SCHEMA_VERSION":1`, 1),
		"bad digest":             strings.Replace(base, "sha256:"+strings.Repeat("a", 64), "sha256:abc", 1),
		"bad hash":               strings.Replace(base, "sha256:"+strings.Repeat("b", 64), "not-a-hash", 1),
		"bad fingerprint":        strings.Replace(base, "SHA256:"+strings.Repeat("A", 43), "SHA256:abc", 1),
		"zero port":              strings.Replace(base, `"value":20000`, `"value":0`, 1),
		"large port":             strings.Replace(base, `"value":20000`, `"value":65536`, 1),
		"negative port":          strings.Replace(base, `"value":20000`, `"value":-1`, 1),
		"negative generation":    strings.Replace(base, `"generation":{"status":"known","value":4}`, `"generation":{"status":"known","value":-1}`, 1),
		"unknown with value":     strings.Replace(base, `"status":"known","value":4`, `"status":"unknown","value":4`, 1),
		"missing observed field": strings.Replace(base, `"cgroup_v2":{"status":"known","value":true},`, ``, 1),
		"null value":             strings.Replace(base, `"status":"known","value":4`, `"status":"known","value":null`, 1),
		"null array":             strings.Replace(base, `"value":[20000]`, `"value":null`, 1),
		"unsafe name":            strings.Replace(base, `"name":"hello"`, `"name":"../hello"`, 1),
		"extra secret value":     strings.Replace(base, `"name":"hello-key"`, `"value":"must-not-appear","name":"hello-key"`, 1),
		"trailing json":          base + `{}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(input)); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

func TestUnknownAndAbsent(t *testing.T) {
	s, err := Decode(fixture(t, "fresh-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	s.FreeDiskBytes = Observation[uint64]{Status: Unknown}
	s.Runner.Linger = Observation[bool]{Status: Unsupported}
	s.Versions.Podman = Observation[string]{Status: Absent}
	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.FreeDiskBytes.Status != Unknown || decoded.Runner.Linger.Status != Unsupported || decoded.Versions.Podman.Status != Absent {
		t.Fatal("observation status lost")
	}
	s.Generation = Observation[uint64]{Status: Absent}
	if _, err := Encode(s); err == nil {
		t.Fatal("absent generation accepted")
	}
}

func TestUnsupportedArchitecture(t *testing.T) {
	s, err := Decode(fixture(t, "fresh-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	s.Arch = "riscv64"
	var unsupported *UnsupportedError
	if !errors.As(s.Validate(), &unsupported) {
		t.Fatal("unsupported architecture accepted")
	}
}

func TestSetAndObservationValidation(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"duplicate app":       func(s *Snapshot) { *s.Apps.Value = append(*s.Apps.Value, (*s.Apps.Value)[0]) },
		"duplicate used port": func(s *Snapshot) { *s.UsedPorts.Value = []Port{20000, 20000} },
		"bad used port":       func(s *Snapshot) { *s.UsedPorts.Value = []Port{65536} },
		"duplicate unit": func(s *Snapshot) {
			a := &(*s.Apps.Value)[0]
			*a.QuadletUnits.Value = append(*a.QuadletUnits.Value, (*a.QuadletUnits.Value)[0])
		},
		"duplicate secret": func(s *Snapshot) {
			a := &(*s.Apps.Value)[0]
			*a.Secrets.Value = append(*a.Secrets.Value, (*a.Secrets.Value)[0])
		},
		"unsafe unit":         func(s *Snapshot) { (*(*s.Apps.Value)[0].QuadletUnits.Value)[0].Name = "../bad.container" },
		"bad image platform":  func(s *Snapshot) { (*s.Apps.Value)[0].Image.Value.Platform.Arch = "riscv64" },
		"known without value": func(s *Snapshot) { s.CgroupV2 = Observation[bool]{Status: KnownStatus} },
		"invalid status":      func(s *Snapshot) { s.CgroupV2 = Observation[bool]{Status: "not-measured"} },
		"nil known apps":      func(s *Snapshot) { s.Apps = Known[[]App](nil) },
		"absent with value":   func(s *Snapshot) { s.Versions.Podman.Status = Absent },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := Decode(fixture(t, "one-app"))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("Validate accepted invalid value")
			}
			if _, err := Encode(s); err == nil {
				t.Fatal("Encode accepted invalid value")
			}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(raw); err == nil {
				t.Fatal("Decode accepted invalid value")
			}
		})
	}
}

func TestRequiredFieldsAndExactKeys(t *testing.T) {
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t, "fresh-arm64"), &wire); err != nil {
		t.Fatal(err)
	}
	for key := range wire {
		t.Run(key, func(t *testing.T) {
			original := wire[key]
			delete(wire, key)
			raw, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			wire[key] = original
			if _, err := Decode(raw); err == nil {
				t.Fatal("accepted missing required field")
			}
		})
	}
}

func FuzzDecodeCanonical(f *testing.F) {
	for _, name := range []string{"fresh-arm64", "one-app", "port-conflict", "unsupported-ubuntu"} {
		b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Decode(data)
		if err != nil {
			return
		}
		canonical, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Decode(canonical)
		if err != nil {
			t.Fatal(err)
		}
		canonical2, err := Encode(again)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonical, canonical2) {
			t.Fatal("canonical encoding is not stable")
		}
	})
}

func TestCanonicalObjectFieldOrdering(t *testing.T) {
	original := fixture(t, "one-app")
	var object map[string]any
	if err := json.Unmarshal(original, &object); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Decode(reordered)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, canonical) {
		t.Fatal("object field ordering or whitespace affects canonical bytes")
	}
}

func TestMeasuredZeroAndLargeCounters(t *testing.T) {
	s, err := Decode(fixture(t, "fresh-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	s.Generation = Known(^uint64(0))
	s.FreeDiskBytes = Known(uint64(0))
	s.CgroupV2 = Known(false)
	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if *again.Generation.Value != ^uint64(0) || *again.FreeDiskBytes.Value != 0 || *again.CgroupV2.Value {
		t.Fatal("measured values lost precision or presence")
	}
}
