//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestImmutableSchemaRegistry(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	definitions := []data.SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: strings.Repeat("a", 64)}}
	if err = s.RegisterSchemaDefinitions(ctx, reserved.Database.IncarnationID, definitions); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterSchemaDefinitions(ctx, reserved.Database.IncarnationID, definitions); err != nil {
		t.Fatal("idempotent registration", err)
	}
	ro, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ro.Close(); err != nil {
			t.Error(err)
		}
	})
	got, err := ro.ReadSchemaDefinitions(ctx, reserved.Database.IncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	want := []data.SchemaDefinition{{Database: "main", Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, definitions[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions: %+v", got)
	}
	definitions[0].CatalogSHA256 = strings.Repeat("b", 64)
	if err = s.RegisterSchemaDefinitions(ctx, reserved.Database.IncarnationID, definitions); !errors.Is(err, ErrConflict) {
		t.Fatalf("marker remapped: %v", err)
	}
	definitions[0].Database = "missing"
	if err = s.RegisterSchemaDefinitions(ctx, reserved.Database.IncarnationID, definitions); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign database: %v", err)
	}
	if _, err = s.db.Exec("DELETE FROM data_schema_definitions"); err == nil {
		t.Fatal("registry deletion accepted")
	}
}

// The empty marker reserved with the database counts toward the accumulated
// registry cap. Sixteen individually valid batches must not corrupt the reader.
func TestSchemaRegistryRejectsAccumulatedLimitTransactionally(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	incarnation := reserved.Database.IncarnationID
	var rejected []data.SchemaDefinition
	for batch := 0; batch < 16; batch++ {
		definitions := make([]data.SchemaDefinition, 128)
		for i := range definitions {
			definitions[i] = data.SchemaDefinition{Database: "main", Marker: fmt.Sprintf("v%04d", batch*128+i), CatalogSHA256: strings.Repeat("a", 64)}
		}
		err := s.RegisterSchemaDefinitions(ctx, incarnation, definitions)
		if batch < 15 {
			if err != nil {
				t.Fatalf("valid batch %d: %v", batch, err)
			}
		} else {
			rejected = definitions
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "schema registry") {
				t.Fatalf("overflowing batch must be clearly refused before commit: %v", err)
			}
		}
	}
	definitions, err := s.ReadSchemaDefinitions(ctx, incarnation)
	if err != nil || len(definitions) != 1+15*128 {
		t.Fatalf("refused batch changed readable registry: %d, %v", len(definitions), err)
	}
	for _, definition := range definitions {
		if definition.Marker >= rejected[0].Marker {
			t.Fatalf("partial overflowing batch committed: %+v", definition)
		}
	}
	// Exactly 2,048 entries remain usable, and exact retries do not consume space.
	if err := s.RegisterSchemaDefinitions(ctx, incarnation, rejected[:127]); err != nil {
		t.Fatal("exact capacity refused", err)
	}
	if err := s.RegisterSchemaDefinitions(ctx, incarnation, rejected[:127]); err != nil {
		t.Fatal("idempotent retry at capacity refused", err)
	}
	if err := s.RegisterSchemaDefinitions(ctx, incarnation, rejected[127:]); !errors.Is(err, ErrInvalid) {
		t.Fatal("one entry over capacity admitted", err)
	}
	definitions, err = s.ReadSchemaDefinitions(ctx, incarnation)
	if err != nil || len(definitions) != 2048 {
		t.Fatal("capacity registry unreadable", len(definitions), err)
	}
	// The reader still detects corruption inserted outside registration.
	if _, err := s.db.ExecContext(ctx, "INSERT INTO data_schema_definitions VALUES(?,?,?)", reserved.Database.DatabaseID, rejected[127].Marker, rejected[127].CatalogSHA256); err != nil {
		t.Fatal(err)
	}
	var integrity *IntegrityError
	if _, err := s.ReadSchemaDefinitions(ctx, incarnation); !errors.As(err, &integrity) {
		t.Fatal("oversized corrupt registry admitted", err)
	}
}
