package restore

import (
	"strings"
	"testing"
)

func TestDerivedSchemaMarkers(t *testing.T) {
	for _, marker := range []string{"v1", "sha256:688d95e9133c", "sha256:123", "sha256:688D95E9133C", "sha256:688d95e9133c0"} {
		got := validSchema(SQLiteSchemaObservation{State: VerifiedSchema, Marker: marker, CatalogSHA256: strings.Repeat("a", 64)})
		want := marker == "v1" || marker == "sha256:688d95e9133c"
		if got != want {
			t.Fatalf("marker %q accepted=%v", marker, got)
		}
	}
}
