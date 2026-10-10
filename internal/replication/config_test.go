package replication

import (
	"strings"
	"testing"
	"time"
)

func testBinding() Binding {
	return Binding{Cadence: testDefaultCadence(), DatabaseID: strings.Repeat("1", 32), BindingID: strings.Repeat("2", 32), EpochID: strings.Repeat("3", 32), IncarnationID: strings.Repeat("4", 32), DBPath: "/srv/data/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/app.db", SocketPath: "/srv/state/replication/" + strings.Repeat("2", 32) + "/control.sock", Endpoint: "https://objects.example.invalid", Bucket: "backup-bucket", Prefix: "base/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/epochs/" + strings.Repeat("3", 32) + "/", Region: "auto", ForcePathStyle: true}
}

func TestConfigRoundTrip(t *testing.T) {
	b := testBinding()
	raw, err := RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseConfig(raw, b); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"access-key", "secret-access", "session-token", "replicas:", "url:"} {
		if strings.Contains(string(raw), word) {
			t.Fatalf("forbidden config field %s", word)
		}
	}
}
func TestConfigRejects(t *testing.T) {
	b := testBinding()
	raw, err := RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	base := string(raw)
	cases := map[string]string{
		"typo":               base + "retentoin: false\n",
		"duplicate":          base + "l0-retention: 1h\n",
		"zero":               strings.Replace(base, "l0-retention: 24h", "l0-retention: 0", 1),
		"malformed-duration": strings.Replace(base, "l0-retention: 24h", "l0-retention: soon", 1),
		"negative-duration":  strings.Replace(base, "l0-retention: 24h", "l0-retention: -1h", 1),
		"missing-region":     strings.Replace(base, "region: auto", "", 1),
		"missing-path-style": strings.Replace(base, "force-path-style: true", "", 1),
		"wrong-epoch":        strings.ReplaceAll(base, b.EpochID, strings.Repeat("5", 32)),
		"wrong-socket":       strings.ReplaceAll(base, "control.sock", "other.sock"),
		"credentials":        strings.Replace(base, "type: s3", "type: s3\n        access-key-id: secret", 1),
		"retention":          strings.Replace(base, "enabled: false", "enabled: true", 1),
		"interpolation":      strings.Replace(base, "region: auto", "region: ${AWS_REGION}", 1),
		"alias":              base + "extra: &alias { value: true }\n",
		"extra-document":     base + "---\ndbs: []\n",
		"multiple-db":        strings.Replace(base, "dbs:\n", "dbs:\n    - path: /other/app.db\n", 1),
		"plural-replica":     strings.Replace(base, "replica:", "replicas:", 1),
		"file-replica":       strings.Replace(base, "type: s3", "type: file", 1),
		"duplicate-nested":   strings.Replace(base, "region: auto", "region: auto\n            region: auto", 1),
		"missing-retention":  strings.Replace(base, "enabled: false", "", 1),
		"public-socket":      strings.Replace(base, "permissions: 384", "permissions: 438", 1),
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig([]byte(invalid), b); err == nil {
				t.Fatal("accepted unsafe config")
			}
		})
	}
}
func TestConfigRejectsUnsafeBinding(t *testing.T) {
	for _, mutate := range []func(*Binding){func(b *Binding) { b.Endpoint = "http://objects.example.invalid" }, func(b *Binding) { b.Endpoint = "https://user:secret@objects.example.invalid" }, func(b *Binding) { b.DBPath = "/srv/../app.db" }, func(b *Binding) { b.Prefix = "wrong/" }, func(b *Binding) { b.BindingID = "other" }} {
		b := testBinding()
		mutate(&b)
		if _, err := RenderConfig(b); err == nil {
			t.Fatal("accepted invalid binding")
		}
	}
}

func TestConfigRequiresStderrLogging(t *testing.T) {
	b := testBinding()
	raw, err := RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "logging:\n    stderr: true\n") {
		t.Fatal("generated config does not isolate diagnostics from JSON stdout")
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), "logging:\n    stderr: true\n", "", 1),
		strings.Replace(string(raw), "stderr: true", "stderr: false", 1),
		strings.Replace(string(raw), "stderr: true", "stderr: true\n    stdout: true", 1),
		strings.Replace(string(raw), "stderr: true", "stderr: true\n    stderr: true", 1),
	} {
		if _, err := ParseConfig([]byte(invalid), b); err == nil {
			t.Fatal("accepted unsafe logging config")
		}
	}
}

func TestConfigUsesTypedCadence(t *testing.T) {
	b := testBinding()
	b.Cadence = testDefaultCadence()
	raw, err := RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "sync-interval: 1m0s") || !strings.Contains(string(raw), "snapshot:\n    interval: 6h0m0s") {
		t.Fatal("missing explicit relaxed sync and full snapshot cadence", string(raw))
	}
	b.Cadence.SyncInterval = 10 * time.Second
	b.Cadence.SnapshotInterval = 12 * time.Hour
	raw, err = RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "sync-interval: 10s") || !strings.Contains(string(raw), "interval: 12h0m0s") {
		t.Fatal("typed cadence ignored")
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), "sync-interval: 10s", "sync-interval: 1m", 1),
		strings.Replace(string(raw), "interval: 12h0m0s", "interval: 6h", 1),
		strings.Replace(string(raw), "snapshot:\n    interval: 12h0m0s\n", "", 1),
	} {
		if _, err := ParseConfig([]byte(invalid), b); err == nil {
			t.Fatal("accepted config cadence mismatch")
		}
	}
	for _, sync := range []time.Duration{0, -time.Second, time.Second, time.Hour + time.Second} {
		b.Cadence.SyncInterval = sync
		if _, err := RenderConfig(b); err == nil {
			t.Fatal("accepted out-of-range sync interval", sync)
		}
	}
	b.Cadence = testDefaultCadence()
	b.Cadence.SnapshotInterval = 0
	if _, err := RenderConfig(b); err == nil {
		t.Fatal("accepted missing snapshot cadence")
	}
}

func TestCadenceAbsoluteSnapshotBounds(t *testing.T) {
	for _, interval := range []time.Duration{time.Hour - time.Nanosecond, 24*time.Hour + time.Nanosecond} {
		c := testDefaultCadence()
		c.SnapshotInterval = interval
		if c.Validate() == nil {
			t.Fatal("outside absolute snapshot bound", interval)
		}
	}
	for _, interval := range []time.Duration{time.Hour, 24 * time.Hour} {
		c := testDefaultCadence()
		c.SnapshotInterval = interval
		if c.Validate() != nil {
			t.Fatal("absolute snapshot boundary refused", interval)
		}
	}
}

func testDefaultCadence() Cadence {
	return Cadence{SyncInterval: time.Minute, SnapshotInterval: 6 * time.Hour}
}

func TestSocketPathExactKernelBoundary(t *testing.T) {
	for _, size := range []int{107, 108} {
		t.Run(strings.Repeat("x", size-107)+"boundary", func(t *testing.T) {
			binding := testBinding()
			suffix := "/replication/" + binding.BindingID + "/control.sock"
			binding.SocketPath = "/" + strings.Repeat("x", size-len(suffix)-1) + suffix
			if len(binding.SocketPath) != size {
				t.Fatal("wrong fixture boundary")
			}
			_, err := RenderConfig(binding)
			if size == 107 && err != nil {
				t.Fatalf("107-byte path refused: %v", err)
			}
			if size == 108 && err == nil {
				t.Fatal("108-byte socket path accepted")
			}
		})
	}
}
