package spec

import (
	"strings"
	"testing"
)

func TestReservedEnvironmentNamespace(t *testing.T) {
	for _, section := range []string{"environment", "secrets"} {
		for _, key := range []string{"BRINE_DB_MAIN_PATH", "BRINE_OTHER"} {
			raw := persistentSpec(t) + "\n[" + section + "]\n" + key + "=\"token\"\n"
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("accepted reserved %s key %s", section, key)
			}
		}
	}
}

func TestDatabaseEnvironmentNameCollisions(t *testing.T) {
	for _, name := range []string{"main-db", "MAIN-DB", "main_db"} {
		raw := strings.ReplaceAll(persistentSpec(t), `name="main"`, `name="main-db"`)
		raw += `
[[databases]]
name="` + name + `"
persistent_root="/srv/brine-data"
mount_path="/other"
filename="app.db"
backup_destination="primary"
`
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted colliding or invalid database name %s", name)
		}
	}
}

func TestAppEnvironmentFilesUnsupported(t *testing.T) {
	for _, key := range []string{"environment_file", "env_file"} {
		raw := key + "=\"settings.env\"\n" + persistentSpec(t)
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted unsupported %s", key)
		}
	}
}
