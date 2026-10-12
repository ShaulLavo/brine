//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestArchiveFactsReusesPreparationProofWithoutProbing(t *testing.T) {
	ctx := context.Background()
	state, desired, facts := persistentHostFixture(t)
	database := collectPersistent(t, facts, desired).Database
	inspected, err := data.InspectRoot(string(database.Root))
	if err != nil || inspected.POSIXLocks || inspected.DurableRename {
		t.Fatal("read-only inspection must not invent capabilities", inspected, err)
	}
	raw := fmt.Sprintf(`schema_version=1
version="archive-root-fixture"
persistent_roots=[%q]
[resources]
memory_mb=512
pids_limit=128
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
`, string(database.Root))
	pol, err := policy.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	host := archiveHost{service: Service{Store: state, Policy: &fakePolicy{p: pol}, Inventory: &fakeInventory{store: state, snapshot: target.Snapshot{Apps: target.Known([]target.App{})}}}}
	before, err := os.ReadDir(string(database.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.archiveFacts(ctx, string(desired.Name), "", false); err != nil {
		t.Fatal("prepared root refused by real archive facts", err)
	}
	// A failed first writer consumes untouched-empty evidence but leaves the
	// durable preparation proof valid, with no committed release to inspect.
	if err = state.RecordWriterAttempt(ctx, database.IncarnationID, "failed-first-release"); err != nil {
		t.Fatal(err)
	}
	written, err := host.archiveFacts(ctx, string(desired.Name), "", false)
	if err != nil || written.Candidate.ReleaseID != "" || len(written.Candidate.Databases) != 1 || !written.Candidate.Databases[0].Written() {
		t.Fatal("first writer history stranded retained preparation proof", written, err)
	}
	after, err := os.ReadDir(string(database.Root))
	if err != nil || len(after) != len(before) {
		t.Fatal("planning created probe artifacts", err)
	}
	for i, entry := range after {
		if entry.Name() != before[i].Name() || strings.HasPrefix(entry.Name(), ".brine-probe-") {
			t.Fatal("planning mutated root", entry.Name())
		}
	}
	// Keep the allocated tree intact while replacing only the enrolled root inode.
	// Its proof must not be transferred to a different directory on the same device.
	moved := string(database.Root) + "-original"
	if err = os.Rename(string(database.Root), moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(string(database.Root)); err != nil {
			t.Error(err)
		}
		if err := os.Rename(moved, string(database.Root)); err != nil {
			t.Error(err)
		}
	})
	if err = os.Mkdir(string(database.Root), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = host.archiveFacts(ctx, string(desired.Name), "", false); !errors.Is(err, archive.ErrRefused) {
		t.Fatal("replaced root admitted", err)
	}
}
