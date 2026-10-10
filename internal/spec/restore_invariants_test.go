package spec

import (
	"strings"
	"testing"
)

func TestDeclaredRestoreInvariants(t *testing.T) {
	raw := persistentSpec(t) + `
[[restore_invariants]]
database="main"
kind="row_count"
table="orders"
count=7
[[restore_invariants]]
database="main"
kind="integer_range"
table="orders"
column="quantity"
minimum=1
maximum=100
`
	app, err := Parse([]byte(raw))
	if err != nil || len(app.RestoreInvariants) != 2 {
		t.Fatal("typed restore checks lost", err)
	}
	for _, change := range [][2]string{{`table="orders"`, `table="orders;drop"`}, {`kind="row_count"`, `kind="sql"`}, {`count=7`, `count=-1`}, {"database=\"main\"\nkind=\"row_count\"", "database=\"other\"\nkind=\"row_count\""}, {`maximum=100`, `maximum=0`}} {
		if _, err := Parse([]byte(strings.Replace(raw, change[0], change[1], 1))); err == nil {
			t.Fatal("invalid read-only check accepted", change)
		}
	}
}
