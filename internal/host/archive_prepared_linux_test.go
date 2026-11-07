//go:build linux

package host

import (
	"os"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestRemovalPreparedDirectUsesGenuinePointAndRetainsArchive(t *testing.T) {
	r := newRemovalHostRig(t)
	m, err := r.service.Prepare(t.Context(), "hello", r.plan.ID)
	if err != nil || !m.Valid() {
		t.Fatal("prepare", err)
	}
	coverage, ok := m.Coverage[0].SQLite()
	if !ok || coverage.Prepared == nil || coverage.Receipt.OperationID == r.task.ID {
		t.Fatal("lost original preparation identity")
	}
	d, err := r.service.Retire(t.Context(), r.plan.ArchiveID)
	if err != nil || d.Stage != archive.Retired {
		o, _, _ := r.prep.state.ReadArchiveOperation(t.Context(), r.plan.ID)
		t.Log("runtime", r.host.VerifyArchiveRuntime(t.Context(), r.plan), "recheck", r.host.RecheckArchive(t.Context(), r.plan, o, false))
		t.Fatal("retire", d, err)
	}
	receipt := r.receipt(t)
	if receipt.OperationID != r.task.ID || receipt.AttemptOperationID != r.task.ID || !receipt.RetainUntil.Equal(d.CommittedAt.Add(30*24*time.Hour)) {
		t.Fatal(receipt)
	}
	if err = data.VerifyArchivedTree(r.plan.Trees[0], r.plan.Candidate.Incarnation, r.plan.ArchiveID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.host.ArchiveMaintenanceAdmission(t.Context(), r.plan, d); err != nil {
		t.Fatal("original-fence maintenance", err)
	}
	r.noWriterEffects(t)
}

func TestRemovalPreparedRefusesRunningBeforeClaim(t *testing.T) {
	for _, replica := range []bool{false, true} {
		t.Run(map[bool]string{false: "writer", true: "replica"}[replica], func(t *testing.T) { testRemovalRunning(t, replica) })
	}
}
func testRemovalRunning(t *testing.T, replica bool) {
	r := newRemovalHostRig(t)
	name := "hello.service"
	if replica {
		name = r.prep.replicaUnit()
	}
	r.prep.exec.units[name] = "running"
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err == nil {
		t.Fatal("running unit admitted")
	}
	if _, exists, err := r.prep.state.ReadArchiveOperation(t.Context(), r.plan.ID); err != nil || exists {
		t.Fatal("claimed running source", err)
	}
	r.noWriterEffects(t)
}

func TestRemovalPreparedWALChangeBeforeClaimAndRename(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before claim", true: "before rename"}[claimed], func(t *testing.T) { testRemovalWAL(t, claimed) })
	}
}
func testRemovalWAL(t *testing.T, claimed bool) {
	r := newRemovalHostRig(t)
	if claimed {
		if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
			t.Fatal(err)
		}
	}
	replaceRemovalWAL(t, r)
	var err error
	if claimed {
		_, err = r.service.Retire(t.Context(), r.plan.ArchiveID)
	} else {
		_, err = r.service.Prepare(t.Context(), "hello", r.plan.ID)
	}
	if err == nil {
		t.Fatal("changed source accepted")
	}
	if data.VerifyUnarchivedTree(r.plan.Trees[0], r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
		t.Fatal("moved changed source")
	}
	r.noWriterEffects(t)
}

func TestRemovalPreparedSafeAbandonDoesNotReadCredentialPacket(t *testing.T) {
	r := newRemovalHostRig(t)
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	r.resolve(t, "abandon")
	if err := os.RemoveAll(r.prep.root + "/credentials"); err != nil {
		t.Fatal(err)
	}
	d, err := r.service.Abandon(t.Context(), r.plan.ArchiveID)
	if err != nil || d.Stage != archive.Abandoned {
		t.Fatal(d, err)
	}
	if r.receipt(t).Outcome != "abandoned" || r.prep.heldFences() != 0 {
		t.Fatal("abandon did not release exact fence")
	}
	r.noWriterEffects(t)
}

func TestRemovalPreparedTwoDatabasesDoNotReacquireLifetimeLocks(t *testing.T) {
	r := newRemovalHostRigMode(t, true)
	if len(r.plan.Removal.Points) != 2 {
		t.Fatal("missing genuine second preparation")
	}
	m, err := r.service.Prepare(t.Context(), "hello", r.plan.ID)
	if err != nil || len(m.Coverage) != 2 {
		t.Fatal("two-database prepare", err)
	}
	d, err := r.service.Retire(t.Context(), r.plan.ArchiveID)
	if err != nil || d.Stage != archive.Retired {
		t.Fatal("two-database retirement", d, err)
	}
	if r.receipt(t).Outcome != "retired" {
		t.Fatal("lost joined completion")
	}
	r.noWriterEffects(t)
}

func TestRemovalPreparedReleasedLTXReadsWithoutAnotherUpload(t *testing.T) {
	r := newRemovalHostRigSource(t, false, true)
	puts, gets := r.prep.remote.counts()
	m, err := r.service.Prepare(t.Context(), "hello", r.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := m.Coverage[0].SQLite()
	if !ok || c.Point.LTX == nil || c.Receipt.OperationID == r.task.ID {
		t.Fatal("not original released LTX coverage")
	}
	afterPuts, afterGets := r.prep.remote.counts()
	if puts != afterPuts || gets != afterGets {
		t.Fatal("CoverPrepared made a provider request")
	}
	if _, err = r.service.Retire(t.Context(), r.plan.ArchiveID); err != nil {
		t.Fatal(err)
	}
	r.noWriterEffects(t)
}

func TestRemovalMaintenanceDoesNotInspectRecreatedAppWriter(t *testing.T) {
	r := newRemovalHostRig(t)
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	d, err := r.service.Retire(t.Context(), r.plan.ArchiveID)
	if err != nil {
		t.Fatal(err)
	}
	original := sqliteArchiveDatabase(t, r.plan.Candidate.Databases[0])
	declaration := data.SQLiteDatabase{Name: original.Binding.Name, PersistentRoot: original.Binding.Root, MountPath: original.Binding.MountPath, Filename: original.Binding.Filename, BackupDestination: original.Epochs[0].Destination.Reference, SyncInterval: time.Minute}
	fresh, err := r.prep.state.ReserveDatabase(t.Context(), store.DataReservation{App: "hello", PolicyHash: r.plan.PolicyHash, Database: declaration, Destination: original.Epochs[0].Destination})
	if err != nil || fresh.Database.IncarnationID == original.Binding.IncarnationID || fresh.Database.DatabaseID == original.Binding.DatabaseID {
		t.Fatal("fresh incarnation", err)
	}
	r.recorder.newWriter = true
	if _, err = r.host.ArchiveMaintenanceAdmission(t.Context(), r.plan, d); err != nil {
		t.Fatal("old archive followed recreated app", err)
	}
	health, err := r.host.ArchiveCredentialHealth(t.Context(), r.plan)
	if err != nil || len(health) != 1 || health[0].BindingID != original.Binding.ReplicaBindingID || health[0].Missing {
		t.Fatal("old exact credentials", health, err)
	}
	r.noWriterEffects(t)
}
