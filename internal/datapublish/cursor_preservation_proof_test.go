package datapublish

import (
	"strings"
	"testing"
)

func TestSourcePreservationProofBelongsToDecodedTerminalCursor(t *testing.T) {
	cursor := cursorFixture(t, true)
	if cursor.SourcePreservationProofSHA256() != "" {
		t.Fatal("unfinished cursor supplied preservation proof")
	}
	hash := strings.Repeat("b", 64)
	preserved, err := cursor.PreserveSource(hash)
	if err != nil || preserved.SourcePreservationProofSHA256() != hash {
		t.Fatal("terminal cursor lost its exact preservation proof", err)
	}
	raw, err := preserved.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCursor(raw, preserved.plan, recordSHA256(raw))
	if err != nil || decoded.SourcePreservationProofSHA256() != hash || cursor.SourcePreservationProofSHA256() != "" {
		t.Fatal("decoded terminal proof or original cursor changed", err)
	}
}
