package plan

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestFreeDiskDecision(t *testing.T) {
	const minimum uint64 = 1 << 30
	in := fixture(t, "ready-arm64")
	in.Snapshot.FreeDiskBytes = target.Known(minimum)
	baseline := build(t, in)
	if baseline.Kind != Create {
		t.Fatal(baseline)
	}
	for _, free := range []uint64{minimum + 1, minimum + 4096, minimum * 2, ^uint64(0)} {
		in.Snapshot.FreeDiskBytes = target.Known(free)
		next := build(t, in)
		if !reflect.DeepEqual(baseline, next) {
			t.Fatalf("free disk %d changed a sufficient-disk plan", free)
		}
	}
	in.Snapshot.FreeDiskBytes = target.Known(minimum - 1)
	refused := build(t, in)
	if refused.Kind != Conflict || len(refused.Changes) != 0 || refused.Hash == baseline.Hash {
		t.Fatal("insufficient disk did not change the plan into a conflict", refused)
	}
	if !reflect.DeepEqual(refused.Conflicts, []Diagnostic{{Code: "insufficient_disk", Field: "free_disk_bytes"}}) {
		t.Fatal(refused.Conflicts)
	}
	in.Snapshot.FreeDiskBytes = target.Known(uint64(0))
	if next := build(t, in); !reflect.DeepEqual(refused, next) {
		t.Fatal("insufficient measurements must have the same decision", next)
	}
}

func TestUnobservedDiskConflicts(t *testing.T) {
	for _, status := range []target.Status{target.Unknown, target.Unsupported} {
		t.Run(string(status), func(t *testing.T) {
			in := fixture(t, "ready-arm64")
			in.Snapshot.FreeDiskBytes = target.Observation[uint64]{Status: status}
			p := build(t, in)
			code := UnknownFacts
			if status == target.Unsupported {
				code = UnsupportedTarget
			}
			if p.Kind != Conflict || len(p.Changes) != 0 || !reflect.DeepEqual(p.Conflicts, []Diagnostic{{Code: code, Field: "free_disk_bytes"}}) {
				t.Fatal(p)
			}
		})
	}
}

func TestSampledSnapshotDecisionFacts(t *testing.T) {
	values := map[string]func(uint64) any{
		"schema_version": func(n uint64) any { return n + 2 },
		"identity.id":    func(n uint64) any { return fmt.Sprintf("other-target-%d", n) },
		"identity.host_key_fingerprint": func(n uint64) any {
			key := make([]byte, 32)
			key[0] = byte(n + 1)
			return "SHA256:" + base64.RawStdEncoding.EncodeToString(key)
		},
		"os.id":                                    func(uint64) any { return "ubuntu" },
		"os.version":                               func(n uint64) any { return strconv.FormatUint(n+14, 10) },
		"arch":                                     func(uint64) any { return "amd64" },
		"versions.systemd.value":                   func(n uint64) any { return fmt.Sprintf("257.%d", n+1) },
		"versions.podman.value":                    func(n uint64) any { return fmt.Sprintf("5.4.%d", n+3) },
		"versions.passt.value":                     func(n uint64) any { return fmt.Sprintf("0.0~git20250503.587980c-%d", n+1) },
		"versions.caddy.value":                     func(n uint64) any { return fmt.Sprintf("2.6.%d", n+3) },
		"versions.litestream.value":                func(n uint64) any { return fmt.Sprintf("0.3.%d", n+14) },
		"cgroup_v2.value":                          func(uint64) any { return false },
		"runner.user.value":                        func(n uint64) any { return fmt.Sprintf("other%d", n) },
		"runner.linger.value":                      func(uint64) any { return false },
		"generation.value":                         func(n uint64) any { return n + 5 },
		"caddy_config.value.generation":            func(n uint64) any { return n + 3 },
		"caddy_config.value.files.0.name":          func(n uint64) any { return fmt.Sprintf("other%d.caddy", n) },
		"caddy_config.value.files.0.hash":          sampledDigest,
		"apps.value.0.name":                        func(n uint64) any { return fmt.Sprintf("other%d", n) },
		"apps.value.0.image.value.digest":          sampledDigest,
		"apps.value.0.image.value.platform.os":     func(uint64) any { return "windows" },
		"apps.value.0.image.value.platform.arch":   func(uint64) any { return "amd64" },
		"apps.value.0.allocated_host_port.value":   func(n uint64) any { return n + 20001 },
		"apps.value.0.quadlet_units.value.0.name":  func(n uint64) any { return fmt.Sprintf("other%d.container", n) },
		"apps.value.0.quadlet_units.value.0.hash":  sampledDigest,
		"apps.value.0.quadlet_units.value.1.name":  func(n uint64) any { return fmt.Sprintf("other%d.volume", n) },
		"apps.value.0.quadlet_units.value.1.hash":  sampledDigest,
		"apps.value.0.secrets.value.0.name":        func(n uint64) any { return fmt.Sprintf("brine-hello-key-v%d", n+3) },
		"apps.value.0.secrets.value.0.id":          func(n uint64) any { return fmt.Sprintf("changed-secret-%d", n) },
		"apps.value.0.secrets.value.1.name":        func(n uint64) any { return fmt.Sprintf("brine-hello-token-v%d", n+3) },
		"apps.value.0.secrets.value.1.id":          func(n uint64) any { return fmt.Sprintf("changed-token-%d", n) },
		"used_ports.value.0":                       func(n uint64) any { return n + 20001 },
		"live_caddy_files.value.0.name":            func(n uint64) any { return fmt.Sprintf("other%d.caddy", n) },
		"live_caddy_files.value.0.app":             func(n uint64) any { return fmt.Sprintf("other%d", n) },
		"live_caddy_files.value.0.domains.value.0": func(n uint64) any { return fmt.Sprintf("other%d.example.com", n) },
		"port_owners.value.0.port":                 func(n uint64) any { return n + 20001 },
		"port_owners.value.0.app":                  func(n uint64) any { return fmt.Sprintf("other%d", n) },
		"port_owners.value.0.process":              func(n uint64) any { return fmt.Sprintf("proxy%d", n) },
		"port_owners.value.0.unit":                 func(n uint64) any { return fmt.Sprintf("other%d.service", n) },
	}
	original := installed(t)
	baseline := build(t, original)
	originalFacts, err := canonicalDecisionFacts(original.Snapshot, original.Desired.MinimumFreeDiskBytes)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(original.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	document := snapshotDocument(t, wire)
	var paths []string
	snapshotLeafPaths(document, "", &paths)
	slices.Sort(paths)
	for _, path := range paths {
		if path == "free_disk_bytes.value" {
			continue
		}
		status := strings.HasSuffix(path, ".status")
		value, covered := values[path]
		if !status && !covered {
			t.Fatalf("snapshot fact %s has no perturbation; classify it", path)
		}
		t.Run(path, func(t *testing.T) {
			r := rand.New(rand.NewPCG(0, 1))
			for sample := 0; sample < 16; sample++ {
				doc := snapshotDocument(t, wire)
				replacement := any(target.Unknown)
				if status && sample%2 == 1 {
					replacement = target.Unsupported
				}
				if !status {
					replacement = value(r.Uint64N(100))
				}
				changeSnapshotPath(t, doc, path, replacement, status)
				changedWire, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				in := original
				in.Snapshot = target.Snapshot{}
				if err := json.Unmarshal(changedWire, &in.Snapshot); err != nil {
					t.Fatal(err)
				}
				facts, err := canonicalDecisionFacts(in.Snapshot, in.Desired.MinimumFreeDiskBytes)
				if err != nil || hash(facts) == hash(originalFacts) {
					t.Fatalf("sample %d lost %s from hash material: %v", sample, path, err)
				}
				next, err := Build(in)
				if path == "schema_version" || path == "apps.value.0.image.value.platform.os" {
					if err == nil {
						t.Fatal("malformed snapshot accepted")
					}
				} else if err != nil || next.Hash == baseline.Hash {
					t.Fatalf("sample %d changed decision retained plan hash: %v", sample, err)
				}
			}
		})
	}
	for path := range values {
		if !slices.Contains(paths, path) {
			t.Fatalf("perturbation %s no longer reaches a snapshot fact", path)
		}
	}
}

func sampledDigest(n uint64) any { return fmt.Sprintf("sha256:%064x", n+1) }

func snapshotDocument(t *testing.T, wire []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	d := json.NewDecoder(bytes.NewReader(wire))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func snapshotLeafPaths(value any, prefix string, paths *[]string) {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			snapshotLeafPaths(item, path, paths)
		}
	case []any:
		for i, item := range value {
			snapshotLeafPaths(item, prefix+"."+strconv.Itoa(i), paths)
		}
	default:
		*paths = append(*paths, prefix)
	}
}

func changeSnapshotPath(t *testing.T, document map[string]any, path string, replacement any, observationStatus bool) {
	t.Helper()
	segments := strings.Split(path, ".")
	var current any = document
	for _, segment := range segments[:len(segments)-1] {
		switch node := current.(type) {
		case map[string]any:
			current = node[segment]
		case []any:
			i, err := strconv.Atoi(segment)
			if err != nil {
				t.Fatal(err)
			}
			current = node[i]
		default:
			t.Fatal("invalid snapshot path", path)
		}
	}
	last := segments[len(segments)-1]
	switch node := current.(type) {
	case map[string]any:
		node[last] = replacement
		if observationStatus {
			delete(node, "value")
		}
	case []any:
		i, err := strconv.Atoi(last)
		if err != nil {
			t.Fatal(err)
		}
		node[i] = replacement
	default:
		t.Fatal("invalid snapshot path", path)
	}
}

func TestConfiguredDiskMinimumChangesDecision(t *testing.T) {
	in := fixture(t, "ready-arm64")
	minimum := in.Desired.MinimumFreeDiskBytes
	in.Snapshot.FreeDiskBytes = target.Known(minimum)
	baseline := build(t, in)
	in.Desired.MinimumFreeDiskBytes = minimum + 1
	changed := build(t, in)
	if changed.Kind != Conflict || changed.Hash == baseline.Hash || changed.DesiredHash == baseline.DesiredHash || len(changed.Changes) != 0 {
		t.Fatal(changed)
	}
	in.Snapshot.FreeDiskBytes = target.Known(minimum + 1)
	recovered := build(t, in)
	if recovered.Kind != Create || recovered.Hash == baseline.Hash {
		t.Fatal(recovered)
	}
}

func TestSampledDiskMeasurementsPreserveEveryPlanKind(t *testing.T) {
	for name, in := range goldenInputs(t) {
		t.Run(name, func(t *testing.T) {
			minimum := in.Desired.MinimumFreeDiskBytes
			in.Snapshot.FreeDiskBytes = target.Known(minimum)
			baseline := build(t, in)
			r := rand.New(rand.NewPCG(0, 1))
			for sample := 0; sample < 100; sample++ {
				in.Snapshot.FreeDiskBytes = target.Known(minimum + r.Uint64N(^uint64(0)-minimum))
				next := build(t, in)
				if !reflect.DeepEqual(baseline, next) {
					t.Fatalf("sample %d changed plan within sufficient disk class", sample)
				}
			}
		})
	}
}

func TestSnapshotFieldsHaveExplicitClassification(t *testing.T) {
	classified := map[reflect.Type][]string{
		reflect.TypeFor[target.Snapshot]():          {"schema_version", "identity", "os", "arch", "versions", "cgroup_v2", "runner", "generation", "caddy_config", "apps", "used_ports", "live_caddy_files", "port_owners", "free_disk_bytes"},
		reflect.TypeFor[target.Identity]():          {"id", "host_key_fingerprint"},
		reflect.TypeFor[target.OS]():                {"id", "version"},
		reflect.TypeFor[target.Versions]():          {"systemd", "podman", "passt", "caddy", "litestream"},
		reflect.TypeFor[target.Runner]():            {"user", "linger"},
		reflect.TypeFor[target.CaddyConfigSet]():    {"generation", "files"},
		reflect.TypeFor[target.CaddyFile]():         {"name", "hash"},
		reflect.TypeFor[target.App]():               {"name", "image", "allocated_host_port", "quadlet_units", "secrets"},
		reflect.TypeFor[target.Image]():             {"digest", "platform"},
		reflect.TypeFor[target.Platform]():          {"os", "arch"},
		reflect.TypeFor[target.Unit]():              {"name", "hash"},
		reflect.TypeFor[target.Secret]():            {"name", "id"},
		reflect.TypeFor[target.LiveCaddyFile]():     {"name", "app", "domains"},
		reflect.TypeFor[target.PortOwner]():         {"port", "app", "process", "unit"},
		reflect.TypeFor[target.Observation[bool]](): {"status", "value"},
	}
	for typ, want := range classified {
		t.Run(typ.Name(), func(t *testing.T) {
			var fields []string
			for field := range typ.Fields() {
				fields = append(fields, strings.Split(field.Tag.Get("json"), ",")[0])
			}
			if !slices.Equal(fields, want) {
				t.Fatalf("inventory shape changed; classify every new field before hashing: got %v, classified %v", fields, want)
			}
		})
	}
}

func TestZeroDesiredMinimumCannotDisableDiskSafety(t *testing.T) {
	in := fixture(t, "ready-arm64")
	in.Desired.MinimumFreeDiskBytes = 0
	in.Snapshot.FreeDiskBytes = target.Known(uint64(0))
	p := build(t, in)
	if p.Kind != Conflict || !slices.Contains(p.Conflicts, Diagnostic{Code: InsufficientDisk, Field: "free_disk_bytes"}) || len(p.Changes) != 0 {
		t.Fatal(p)
	}
}
