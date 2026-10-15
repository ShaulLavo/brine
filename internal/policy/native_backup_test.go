package policy

import (
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestNativeBackupDependencies(t *testing.T) {
	app, _ := managedInputs(t, false)
	raw, err := os.ReadFile("testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw) + `
[[backup_retention]]
destination = "primary"
endpoint = "https://storage.example.com"
bucket = "fixture-backups"
base_prefix = "fixture"
verification_id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
verified_at = "2026-10-15T00:00:00Z"
freshness_seconds = 3600
no_object_expiration = true
`
	active, err := Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := Normalize(app, active)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, before, after string
		admitted            bool
	}{
		{"revision", "managed-preview-1", "managed-preview-2", true},
		{"restore grant", "schema_version = 1", "schema_version = 1\nallow_live_restore = true", true},
		{"destination", "https://storage.example.com", "https://elsewhere.example.com", false},
		{"retention", "freshness_seconds = 3600", "freshness_seconds = 7200", false},
		{"cadence", "capture_interval_seconds = 86400", "capture_interval_seconds = 86401", false},
		{"maintenance limit", "max_maintenance_seconds = 120", "max_maintenance_seconds = 121", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkNativeBackupDependencies(t, strings.ReplaceAll(source, tc.before, tc.after), desired, tc.admitted)
		})
	}
}

func checkNativeBackupDependencies(t *testing.T, source string, desired Desired, admitted bool) {
	t.Helper()
	current, err := Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	err = current.CheckNativeBackup(desired)
	if (err == nil) != admitted || err != nil && result.Classify(err).Code() != result.PolicyRefused {
		t.Fatalf("admitted = %v, want %v: %v", err == nil, admitted, err)
	}
}
