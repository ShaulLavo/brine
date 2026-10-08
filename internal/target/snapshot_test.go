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
	for _, name := range []string{"fresh-arm64", "ready-arm64", "one-app", "port-conflict", "missing-passt", "unknown-runtime", "cgroup-v1", "unsupported-ubuntu"} {
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
		"extra secret value":     strings.Replace(base, `"name":"brine-hello-key-v1"`, `"value":"must-not-appear","name":"brine-hello-key-v1"`, 1),
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
	for _, name := range []string{"fresh-arm64", "ready-arm64", "one-app", "port-conflict", "missing-passt", "unknown-runtime", "cgroup-v1", "unsupported-ubuntu"} {
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

func TestCaddyGenerationAndExtraFiles(t *testing.T) {
	s, err := Decode(fixture(t, "one-app"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	controlGeneration := *s.Generation.Value
	s.CaddyConfig.Value.Generation++
	changed, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(original, changed) || *s.Generation.Value != controlGeneration {
		t.Fatal("Caddy generation is not independent hash input")
	}
	s.CaddyConfig.Value.Files = append(s.CaddyConfig.Value.Files, CaddyFile{Name: "stale.caddy", Hash: "sha256:" + strings.Repeat("e", 64)})
	extra, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(changed, extra) {
		t.Fatal("extra Caddy file disappeared from hash input")
	}
	again, err := Decode(extra)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.CaddyConfig.Value.Files) != 2 || len(*again.Apps.Value) != 1 {
		t.Fatal("extra file cannot be represented independently of apps")
	}
	files := s.CaddyConfig.Value.Files
	files[0], files[1] = files[1], files[0]
	before, _ := json.Marshal(s)
	reordered, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(extra, reordered) || !bytes.Equal(before, after) {
		t.Fatal("Caddy files are not sorted without mutation")
	}
}

func TestCaddyObservationStates(t *testing.T) {
	s, err := Decode(fixture(t, "fresh-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{Absent, Unknown, Unsupported} {
		s.CaddyConfig = Observation[CaddyConfigSet]{Status: status}
		b, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		if again.CaddyConfig.Status != status || again.CaddyConfig.Value != nil {
			t.Fatal("Caddy observation status lost")
		}
	}
	s.CaddyConfig = Known(CaddyConfigSet{Generation: 0, Files: []CaddyFile{}})
	if _, err := Encode(s); err != nil {
		t.Fatal(err)
	}
}

func TestPasstObservationStates(t *testing.T) {
	s, err := Decode(fixture(t, "one-app"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Versions.Passt.Status != KnownStatus {
		t.Fatal("fixture lacks installed passt")
	}
	for _, status := range []Status{Absent, Unknown, Unsupported} {
		s.Versions.Passt = Observation[string]{Status: status}
		b, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(installed, b) {
			t.Fatal("passt prerequisite missing from hash input")
		}
		again, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		if again.Versions.Passt.Status != status || again.Versions.Passt.Value != nil {
			t.Fatal("passt observation status lost")
		}
	}
}

func TestInvalidCaddyConfig(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"duplicate file":    func(s *Snapshot) { files := &s.CaddyConfig.Value.Files; *files = append(*files, (*files)[0]) },
		"unsafe filename":   func(s *Snapshot) { s.CaddyConfig.Value.Files[0].Name = "../hello.caddy" },
		"invalid extension": func(s *Snapshot) { s.CaddyConfig.Value.Files[0].Name = "hello.conf" },
		"invalid hash":      func(s *Snapshot) { s.CaddyConfig.Value.Files[0].Hash = "sha256:abc" },
		"null files":        func(s *Snapshot) { s.CaddyConfig.Value.Files = nil },
		"absent with value": func(s *Snapshot) { s.CaddyConfig.Status = Absent },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := Decode(fixture(t, "one-app"))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("Validate accepted invalid Caddy config")
			}
			if _, err := Encode(s); err == nil {
				t.Fatal("Encode accepted invalid Caddy config")
			}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(raw); err == nil {
				t.Fatal("Decode accepted invalid Caddy config")
			}
		})
	}
}

func TestCaddyConfigRequiredFields(t *testing.T) {
	base := string(fixture(t, "one-app"))
	cases := map[string]string{
		"missing generation":  strings.Replace(base, `"generation":2,`, "", 1),
		"negative generation": strings.Replace(base, `"generation":2,`, `"generation":-1,`, 1),
		"missing files":       strings.Replace(base, `,"files":[{"name":"hello.caddy","hash":"sha256:`+strings.Repeat("d", 64)+`"}]`, "", 1),
		"missing passt":       strings.Replace(base, `"passt":{"status":"known","value":"0.0~git20250503.587980c"},`, "", 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if input == base {
				t.Fatal("test did not change input")
			}
			if _, err := Decode([]byte(input)); err == nil {
				t.Fatal("accepted missing or invalid Caddy/prerequisite field")
			}
		})
	}
}

func TestSecretNamesAreActualPodmanNames(t *testing.T) {
	s, err := Decode(fixture(t, "one-app"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := (*s.Apps.Value)[0].Secrets.Value
	if (*secrets)[0].Name != "brine-hello-key-v1" || (*secrets)[1].Name != "brine-hello-token-v2" {
		t.Fatal("fixture must contain exact versioned Podman names")
	}
	versioned, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	(*secrets)[0].Name = "brine-hello-key-v2"
	rotated, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(versioned, rotated) {
		t.Fatal("secret version is not hash input")
	}
	(*secrets)[0].Name = "legacy-key"
	legacy, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(legacy); err != nil {
		t.Fatal("legacy metadata must remain observable")
	}
}

func TestObservedOwnershipContract(t *testing.T) {
	s, err := Decode(fixture(t, "one-app"))
	if err != nil {
		t.Fatal(err)
	}
	s.LiveCaddyFiles = Known([]LiveCaddyFile{{Name: "unrelated.caddy", App: "", Domains: Known([]string{"https://HELLO.EXAMPLE.COM:443", "Other.Example.Net."})}, {Name: "hello.caddy", App: "hello", Domains: Known([]string{"hello.example.com"})}})
	s.PortOwners = Known([]PortOwner{{Port: 20001, App: "", Process: "foreign", Unit: "foreign.service"}, {Port: 20000, App: "hello", Process: "conmon", Unit: "hello.service"}})
	before, _ := json.Marshal(s)
	first, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("Encode mutated live ownership facts")
	}
	if !bytes.Contains(first, []byte(`"domains":{"status":"known","value":["hello.example.com","other.example.net"]}`)) {
		t.Fatalf("domains not canonical: %s", first)
	}
	files := s.LiveCaddyFiles.Value
	(*files)[0], (*files)[1] = (*files)[1], (*files)[0]
	ports := s.PortOwners.Value
	(*ports)[0], (*ports)[1] = (*ports)[1], (*ports)[0]
	again, err := Encode(s)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("ownership order is unstable", err)
	}
}

func TestCanonicalLiveAddresses(t *testing.T) {
	for raw, want := range map[string]string{
		"HELLO.EXAMPLE.COM": "hello.example.com", "https://HELLO.EXAMPLE.COM:443": "hello.example.com",
		"HTTPS://HELLO.EXAMPLE.COM:443": "hello.example.com", "http://hello.example.com:80": "hello.example.com", "hello.example.com.": "hello.example.com", "*.EXAMPLE.COM": "*.example.com", ":443": "*", "localhost": "localhost", "127.0.0.1:8080": "127.0.0.1", "[::1]:443": "::1", "https://[::1]": "::1",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := CanonicalDomain(raw)
			if err != nil || got != want {
				t.Fatalf("got %q %v, want %q", got, err, want)
			}
			again, err := CanonicalDomain(got)
			if err != nil || again != want {
				t.Fatal("canonical domain is not stable", again, err)
			}
			s, err := Decode(fixture(t, "ready-arm64"))
			if err != nil {
				t.Fatal(err)
			}
			s.LiveCaddyFiles = Known([]LiveCaddyFile{{Name: "foreign.caddy", App: "", Domains: Known([]string{raw})}})
			b, err := Encode(s)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			if (*(*decoded.LiveCaddyFiles.Value)[0].Domains.Value)[0] != want {
				t.Fatal("Decode did not canonicalize")
			}
		})
	}
	for _, raw := range []string{"https://user@example.com", "https://hello.example.com/path", "https://hello.example.com?query", "https://hello.example.com?", "https://hello.example.com#", "https://hello.example.com#fragment", "ftp://hello.example.com", "HELLO..EXAMPLE.COM", "hello.example.com:0", "hello.example.com:65536", "hello.example.com:abc", " hello.example.com", "*.bad*.example.com", "hello/example.com"} {
		if _, err := CanonicalDomain(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestInvalidObservedOwnership(t *testing.T) {
	for name, change := range map[string]func(*Snapshot){
		"nil files": func(s *Snapshot) { s.LiveCaddyFiles = Known[[]LiveCaddyFile](nil) },
		"duplicate files": func(s *Snapshot) {
			*s.LiveCaddyFiles.Value = append(*s.LiveCaddyFiles.Value, (*s.LiveCaddyFiles.Value)[0])
		},
		"unsafe identifier": func(s *Snapshot) { (*s.LiveCaddyFiles.Value)[0].Name = "../foreign.caddy" },
		"nil domains":       func(s *Snapshot) { (*s.LiveCaddyFiles.Value)[0].Domains = Known[[]string](nil) },
		"duplicate canonical domain": func(s *Snapshot) {
			(*s.LiveCaddyFiles.Value)[0].Domains = Known([]string{"hello.example.com", "HELLO.EXAMPLE.COM:443"})
		},
		"unknown domains with value": func(s *Snapshot) { (*s.LiveCaddyFiles.Value)[0].Domains.Status = Unknown },
		"bad observed app":           func(s *Snapshot) { (*s.LiveCaddyFiles.Value)[0].App = "../hello" },
		"nil owners":                 func(s *Snapshot) { s.PortOwners = Known[[]PortOwner](nil) },
		"duplicate owner":            func(s *Snapshot) { *s.PortOwners.Value = append(*s.PortOwners.Value, (*s.PortOwners.Value)[0]) },
		"zero port":                  func(s *Snapshot) { (*s.PortOwners.Value)[0].Port = 0 },
		"unsafe process":             func(s *Snapshot) { (*s.PortOwners.Value)[0].Process = "bad/name" },
		"unsafe unit":                func(s *Snapshot) { (*s.PortOwners.Value)[0].Unit = "../hello.service" },
	} {
		t.Run(name, func(t *testing.T) {
			s, err := Decode(fixture(t, "one-app"))
			if err != nil {
				t.Fatal(err)
			}
			change(&s)
			if _, err = Encode(s); err == nil {
				t.Fatal("Encode accepted invalid ownership")
			}
			raw, _ := json.Marshal(s)
			if _, err = Decode(raw); err == nil {
				t.Fatal("Decode accepted invalid ownership")
			}
		})
	}
}

func TestOwnershipExactWireFields(t *testing.T) {
	base := string(fixture(t, "one-app"))
	for name, input := range map[string]string{
		"missing files":         strings.Replace(base, `"live_caddy_files":{"status":"known","value":[{"name":"hello.caddy","app":"hello","domains":{"status":"known","value":["hello.example.com"]}}]},`, "", 1),
		"missing owners":        strings.Replace(base, `"port_owners":{"status":"known","value":[{"port":20000,"app":"hello","process":"conmon","unit":"hello.service"}]},`, "", 1),
		"unknown file field":    strings.Replace(base, `"domains":{"status":"known","value":["hello.example.com"]}`, `"extra":true,"domains":{"status":"known","value":["hello.example.com"]}`, 1),
		"unknown owner field":   strings.Replace(base, `"process":"conmon"`, `"extra":true,"process":"conmon"`, 1),
		"case alias":            strings.Replace(base, `"port_owners"`, `"PORT_OWNERS"`, 1),
		"release not inventory": strings.Replace(base, `"name":"hello","image"`, `"name":"hello","current_release":{"status":"known","value":"release-0001"},"image"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if input == base {
				t.Fatal("test did not alter wire input")
			}
			if _, err := Decode([]byte(input)); err == nil {
				t.Fatal("invalid wire input accepted")
			}
		})
	}
}
