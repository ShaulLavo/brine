//go:build linux

package host

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovalPreparedUnreleasedUsesProtectedSourceExpectation(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(fmt.Sprint(initialized), func(t *testing.T) { checkRemovalUnreleasedSourceExpectation(t, initialized) })
	}
}

func checkRemovalUnreleasedSourceExpectation(t *testing.T, initialized bool) {
	t.Helper()
	r := newUnreleasedSQLiteRig(t)
	if initialized {
		r.initialize(t)
	}
	_, receipt := r.prepare(t, "")
	if receipt == nil {
		t.Fatal("missing genuine preparation")
	}
	removal := r.consume(t)
	if err := removal.guard.Close(); err != nil {
		t.Fatal(err)
	}
	h := archiveHost{service: r.deploy.service, stateRoot: r.physical.root, home: r.physical.home}
	hold, err := h.HoldPrepared(t.Context(), removal.plan)
	if err != nil {
		t.Fatal("reopen consumed source", err)
	}
	t.Cleanup(func() {
		if err := hold.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = hold.Recheck(t.Context()); err != nil {
		t.Fatal("unchanged source refused", err)
	}
	name := "-wal"
	if !initialized {
		name = "-dir/unrelated"
	}
	path := r.physical.path(name)
	if !initialized {
		path = filepath.Join(r.physical.path("-dir"), "unrelated")
	}
	if err = os.WriteFile(path, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = hold.Recheck(t.Context()); err == nil {
		t.Fatal("changed source admitted")
	}
}
