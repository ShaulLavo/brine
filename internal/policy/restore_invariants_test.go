package policy

import (
	"bytes"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestRestoreInvariantsAreDetachedHashMaterial(t *testing.T) {
	d := Desired{RestoreInvariants: []data.RestoreInvariant{{Database: "main", Kind: "row_count", Table: "orders", Count: 7}}}
	before, err := d.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	a := d.App()
	if len(a.RestoreInvariants) != 1 || a.RestoreInvariants[0].Count != 7 || d.Stateless() {
		t.Fatal("restore declaration lost")
	}
	a.RestoreInvariants[0].Count = 8
	after, err := d.CanonicalBytes()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("app aliases desired restore checks")
	}
	d.RestoreInvariants[0].Count = 8
	changed, err := d.CanonicalBytes()
	if err != nil || bytes.Equal(before, changed) {
		t.Fatal("restore check omitted from desired hash")
	}
}
