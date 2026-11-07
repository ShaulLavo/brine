package policy

import (
	"bytes"
	"testing"
)

func TestSQLiteRemovalBoundsAreExplicitAndHashBound(t *testing.T) {
	absent, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := absent.SQLiteRemovalBounds(); ok {
		t.Fatal("invented removal bounds")
	}
	raw := append(fixture(t), []byte("\n[sqlite_archive_removal]\nmax_backup_age_seconds=300\nmax_restore_test_age_seconds=60\nrecovery_window_seconds=86400\n")...)
	allowed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bounds, ok := allowed.SQLiteRemovalBounds(); !ok || bounds.RecoveryWindowSeconds != 86400 || allowed.Hash() == absent.Hash() {
		t.Fatal("bounds not hash-bound")
	}
	for _, invalid := range [][]byte{
		bytes.Replace(raw, []byte("max_backup_age_seconds=300"), []byte("max_backup_age_seconds=0"), 1),
		bytes.Replace(raw, []byte("recovery_window_seconds=86400"), nil, 1),
		append(bytes.Clone(raw), []byte("approved=true\n")...),
	} {
		if _, err = Parse(invalid); err == nil {
			t.Fatal("invalid removal bounds accepted")
		}
	}
}
