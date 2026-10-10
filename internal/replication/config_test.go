package replication

import (
	"strings"
	"testing"
)

func testBinding() Binding {
	return Binding{DatabaseID: strings.Repeat("1", 32), BindingID: strings.Repeat("2", 32), EpochID: strings.Repeat("3", 32), IncarnationID: strings.Repeat("4", 32), DBPath: "/srv/data/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/app.db", SocketPath: "/srv/state/replication/" + strings.Repeat("2", 32) + "/control.sock", Endpoint: "https://objects.example.invalid", Bucket: "backup-bucket", Prefix: "base/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/epochs/" + strings.Repeat("3", 32) + "/", Region: "auto", ForcePathStyle: true}
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
