package policy

import (
	"testing"
)

func TestObsoleteAgentMigrationFlagIsRefused(t *testing.T) {
	raw := fixture(t)
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(append([]byte("allow_agent_migrations=true\n"), raw...)); err == nil {
		t.Fatal("obsolete policy flag accepted")
	}
}

func TestInitializationBoundsRequireExplicitSafeValues(t *testing.T) {
	for _, item := range []struct {
		name, settings string
		valid          bool
	}{
		{"absent", "", false},
		{"valid", "max_backup_age_seconds=300\nmax_restore_test_age_seconds=60\nrecovery_window_seconds=86400\n", true},
		{"zero-backup", "max_backup_age_seconds=0\nmax_restore_test_age_seconds=60\nrecovery_window_seconds=86400\n", false},
		{"missing-window", "max_backup_age_seconds=300\nmax_restore_test_age_seconds=60\n", false},
		{"unbounded-test", "max_backup_age_seconds=300\nmax_restore_test_age_seconds=301\nrecovery_window_seconds=86400\n", false},
		{"unknown-field", "max_backup_age_seconds=300\nmax_restore_test_age_seconds=60\nrecovery_window_seconds=86400\napproved=true\n", false},
	} {
		t.Run(item.name, func(t *testing.T) {
			raw := fixture(t)
			if item.settings != "" {
				raw = append(raw, []byte("\n[data_initialization]\n"+item.settings)...)
			}
			p, err := Parse(raw)
			if item.name == "absent" {
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := p.InitializationBounds(); ok {
					t.Fatal("missing bounds invented")
				}
				return
			}
			if item.valid {
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := p.InitializationBounds(); !ok {
					t.Fatal("explicit bounds refused")
				}
			} else if err == nil {
				t.Fatal("unsafe bounds accepted")
			}
		})
	}
}
