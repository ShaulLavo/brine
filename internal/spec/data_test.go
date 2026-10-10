package spec

import (
	"os"
	"strings"
	"testing"
	"time"
)

func persistentSpec(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + `
[runtime]
uid=10001
gid=10002
[[databases]]
name="main"
persistent_root="/srv/brine-data"
mount_path="/data"
filename="app.db"
backup_destination="primary"
[[schema_compatibility]]
database="main"
accepts=["orders-v2","orders-v1"]
startup="preserve"
[[schema_definitions]]
database="main"
marker="orders-v1"
catalog_sha256="688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"
`
}
func TestPersistentDeclaration(t *testing.T) {
	raw := persistentSpec(t)
	a, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if a.Runtime == nil || a.Runtime.UID != 10001 || a.Runtime.GID != 10002 || len(a.Databases) != 1 || a.Databases[0].SyncInterval != time.Minute || a.SchemaCompatibility[0].Accepts[0] != "orders-v1" {
		t.Fatalf("persistent declaration lost: %+v", a)
	}
	a, err = Parse([]byte(strings.Replace(raw, "filename=\"app.db\"", "filename=\"app.db\"\nsync_interval=\"30s\"", 1)))
	if err != nil || a.Databases[0].SyncInterval != 30*time.Second {
		t.Fatalf("cadence: %v", err)
	}
}
func TestPersistentDeclarationRefusals(t *testing.T) {
	raw := persistentSpec(t)
	for _, tc := range []struct{ old, new string }{
		{"uid=10001", "uid=0"}, {"gid=10002", "gid=65536"}, {"[runtime]\nuid=10001\ngid=10002", ""},
		{"mount_path=\"/data\"", "mount_path=\"/run/secrets\""}, {"filename=\"app.db\"", "filename=\"app.db-wal\""},
		{"filename=\"app.db\"", "filename=\"app.db\"\nSync_Interval=\"30s\""}, {"filename=\"app.db\"", "filename=\"app.db\"\nstorage_quota=100"},
		{"accepts=[\"orders-v2\",\"orders-v1\"]", "accepts=[]"}, {"accepts=[\"orders-v2\",\"orders-v1\"]", "accepts=[\"orders-v1\",\"orders-v1\"]"},
		{"startup=\"preserve\"", "startup=\"migrate\""}, {"database=\"main\"", "database=\"other\""},
		{"filename=\"app.db\"", "filename=\"app.db\"\nsync_interval=\"0s\""},
	} {
		if _, err := Parse([]byte(strings.Replace(raw, tc.old, tc.new, 1))); err == nil {
			t.Fatalf("accepted invalid field replacement %s", tc.old)
		}
	}
	// Missing compatibility remains a plan conflict, not a parser refusal.
	at := strings.Index(raw, "[[schema_compatibility]]")
	a, err := Parse([]byte(raw[:at]))
	if err != nil || len(a.Databases) != 1 || len(a.SchemaCompatibility) != 0 {
		t.Fatalf("missing compatibility failed parsing: %v", err)
	}
}
