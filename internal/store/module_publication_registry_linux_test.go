//go:build linux

package store

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func TestPublicationReviewExplainsReusedSchemaMarker(t *testing.T) {
	f := newNativeAbandonFixture(t)
	raw, err := os.ReadFile("../spacetimedb/testdata/descriptor-additive.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := spacetimedb.DecodeDescriptorV10(raw, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := datapublish.NewCandidate(strings.Repeat("b", 64), strings.Repeat("c", 64), schema)
	if err != nil {
		t.Fatal(err)
	}
	_, runtime, err := f.state.ReadEngineHead(t.Context(), f.point.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.state.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = checkPublicationCandidateRegistry(t.Context(), tx, f.point.DatabaseID, runtime.Module.Schema.Marker, candidate)
	if result.Classify(err).Code() != result.Code("native_schema_marker_conflict") {
		t.Fatal("schema marker reuse returned an opaque error", err)
	}
	if err := checkPublicationCandidateRegistry(t.Context(), tx, f.point.DatabaseID, "game-v2", candidate); err != nil {
		t.Fatal("a distinct schema marker was refused", err)
	}
}
