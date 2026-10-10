//go:build linux

package store

import (
	"context"
	"errors"
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
	defer func() { _ = ro.Close() }()
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
