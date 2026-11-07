package store

import (
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
)

func TestSQLiteUnreleasedTypedDeclarations(t *testing.T) {
	base := policy.Desired{Runtime: &data.RuntimeIdentity{UID: 10001, GID: 10001}, PersistentRoots: []data.PersistentRoot{"/srv/data"}, Databases: []data.SQLiteDatabase{{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}}, BackupDestinations: []data.Destination{{Reference: "primary", Endpoint: "https://storage.example", Bucket: "backup", Region: "r1", BasePrefix: "brine", CredentialRef: "primary"}}, SchemaCompatibility: []data.SQLiteSchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{"v1"}}}, SchemaDefinitions: []data.SQLiteSchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: strings.Repeat("a", 64)}}, RestoreInvariants: []data.RestoreInvariant{{Database: "main", Kind: "row_count", Table: "items", Count: 0}}}
	if err := validateUnreleasedDesired(base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"startup", "duplicate-markers", "too-many-markers", "definition", "duplicate-definition", "invariant", "unknown-database", "duplicate-database", "destination"} {
		t.Run(name, func(t *testing.T) { checkInvalidUnreleasedDeclaration(t, base, name) })
	}
}

func checkInvalidUnreleasedDeclaration(t *testing.T, base policy.Desired, name string) {
	t.Helper()
	desired := base
	desired.SchemaCompatibility = slices.Clone(base.SchemaCompatibility)
	desired.SchemaDefinitions = slices.Clone(base.SchemaDefinitions)
	desired.RestoreInvariants = slices.Clone(base.RestoreInvariants)
	desired.BackupDestinations = slices.Clone(base.BackupDestinations)
	switch name {
	case "startup":
		desired.SchemaCompatibility[0].Startup = "initialize"
	case "duplicate-markers":
		desired.SchemaCompatibility[0].Accepts = []string{"v1", "v1"}
	case "too-many-markers":
		desired.SchemaCompatibility[0].Accepts = make([]string, 129)
	case "definition":
		desired.SchemaDefinitions[0].CatalogSHA256 = "bad"
	case "duplicate-definition":
		desired.SchemaDefinitions = append(desired.SchemaDefinitions, desired.SchemaDefinitions[0])
	case "invariant":
		desired.RestoreInvariants[0].Table = "items;DROP TABLE items"
	case "unknown-database":
		desired.RestoreInvariants[0].Database = "other"
	case "duplicate-database":
		desired.Databases = append(slices.Clone(base.Databases), base.Databases[0])
	case "destination":
		desired.BackupDestinations[0].Endpoint = "http://storage.example"
	}
	if validateUnreleasedDesired(desired) == nil {
		t.Fatal("invalid declaration accepted", name)
	}
}
