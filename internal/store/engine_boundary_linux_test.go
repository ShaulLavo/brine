//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestPersistedSQLiteIdentityRejectsAnEngineTag(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	r, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE data_databases SET canonical=? WHERE id=?", raw, r.Database.DatabaseID); err == nil {
		t.Fatal("immutable database evidence was writable")
	}
	// Corrupt this private fixture only, to exercise read-side integrity refusal.
	if _, err = s.db.ExecContext(ctx, "DROP TRIGGER data_databases_no_update"); err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"spacetimedb", "unknown", "sqlite"} {
		forged := strings.Replace(string(raw), `{`, `{"engine":"`+engine+`",`, 1)
		if _, err = s.db.ExecContext(ctx, "UPDATE data_databases SET canonical=? WHERE id=?", forged, r.Database.DatabaseID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ReadReplicaPermit(ctx, r.Database.DatabaseID); err == nil {
			t.Fatal("foreign engine tag authorized a concrete SQLite record")
		}
		var integrity *IntegrityError
		if !errors.As(err, &integrity) {
			t.Fatal("engine mismatch was not rejected as corrupt evidence", err)
		}
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE data_databases SET canonical=? WHERE id=?", raw, r.Database.DatabaseID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadReplicaPermit(ctx, r.Database.DatabaseID)
	if err != nil || got.Database != r.Database || got.StartupIdentity().Engine != data.EngineSQLite {
		t.Fatal("historical SQLite identity changed after extraction", err)
	}
}
