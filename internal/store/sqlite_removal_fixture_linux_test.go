//go:build linux

package store

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

type removalFixture struct {
	t               *testing.T
	fx              *prepFixture
	databases       []*prepFixture
	policy          policy.Policy
	policyRaw       []byte
	originalBundles []sqliteprep.StoppedBundle
	plan            archive.Plan
	task            ops.Operation
	guard           *SQLiteRemovalGuard
	hosts           []*prepHost
}

func newRemovalFixture(t *testing.T, two bool) *removalFixture {
	t.Helper()
	return newRemovalFixtureMode(t, two, true)
}

func newRemovalFixtureMode(t *testing.T, two, prepared bool) *removalFixture {
	t.Helper()
	return newRemovalFixtureBounds(t, two, prepared, 300, 300)
}

func newRemovalFixtureBounds(t *testing.T, two, prepared bool, backupAge, restoreAge uint32) *removalFixture {
	t.Helper()
	fx := newPrepFixture(t)
	r := &removalFixture{t: t, fx: fx, databases: []*prepFixture{fx}}
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	release, err := fx.s.CurrentRelease(t.Context(), fx.app)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err := fx.s.LoadPlan(t.Context(), release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	e := desired.BackupRetention[0]
	raw = bytes.ReplaceAll(raw, []byte("/srv/brine/data"), []byte("/srv/brine-data"))
	raw = append(raw, []byte(fmt.Sprintf(`
[sqlite_archive_removal]
max_backup_age_seconds=%d
max_restore_test_age_seconds=%d
recovery_window_seconds=86400
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
[[backup_retention]]
destination="primary"
endpoint="https://storage.example"
bucket="backups"
base_prefix="brine"
verification_id=%q
verified_at=%q
freshness_seconds=86400
no_object_expiration=true
`, backupAge, restoreAge, e.VerificationID, e.VerifiedAt))...)
	r.policyRaw = bytes.Clone(raw)
	r.policy, err = policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	desired.PolicyHash = r.policy.Hash()
	r.readmit(fx)
	if two {
		r.addDatabase(&desired)
	}
	in := fixture(t)
	in.Desired = desired
	fx.commitRelease(in, "removal-policy-release")
	if prepared {
		r.prepareSources()
	} else {
		r.clearSourceBundle()
	}
	r.savePlan()
	r.task = r.createTask(ops.SQLiteRetire, "", "")
	return r
}

func (r *removalFixture) readmit(fx *prepFixture) {
	req := dataRequest()
	req.App = fx.app
	req.Database.Name = fx.reserved.Database.Name
	req.Database.MountPath = fx.reserved.Database.MountPath
	req.Database.Filename = fx.reserved.Database.Filename
	req.PolicyHash = r.policy.Hash()
	if _, err := fx.s.ReserveDatabase(r.t.Context(), req); err != nil {
		r.t.Fatal(err)
	}
}

func (r *removalFixture) addDatabase(desired *policy.Desired) {
	t, fx := r.t, r.fx
	req := dataRequest()
	req.App, req.PolicyHash = fx.app, r.policy.Hash()
	req.Database.Name, req.Database.MountPath, req.Database.Filename = "second", "/var/lib/second", "second.db"
	reserved, err := fx.s.ReserveDatabase(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	second := &prepFixture{keys: 100, t: t, s: fx.s, app: fx.app, reserved: reserved, location: fx.location, bundle: fx.bundle}
	second.location.Inode = 29
	second.bundle.Location = second.location
	second.bundle.DB.Inode = 31
	second.bundle.WAL.Inode = 32
	second.commitReplica()
	second.recordCredential(time.Now().UTC().Add(2 * time.Hour))
	allocation := data.AllocationReceipt{DatabaseID: reserved.Database.DatabaseID, IncarnationID: reserved.Database.IncarnationID, RootDevice: 7, RootInode: 8, DirectoryDevice: 7, DirectoryInode: 29, AllocatedAt: time.Now().UTC()}
	if err = fx.s.RecordAllocation(t.Context(), allocation); err != nil {
		t.Fatal(err)
	}
	definition := data.SQLiteSchemaDefinition{Database: "second", Marker: "v1", CatalogSHA256: strings.Repeat("d", 64)}
	if err = fx.s.RegisterSchemaDefinitions(t.Context(), reserved.Database.IncarnationID, []data.SQLiteSchemaDefinition{definition}); err != nil {
		t.Fatal(err)
	}
	desired.Databases = append(desired.Databases, req.Database)
	desired.SchemaCompatibility = append(desired.SchemaCompatibility, data.SQLiteSchemaCompatibility{Database: "second", Startup: "preserve", Accepts: []string{"v1"}})
	desired.SchemaDefinitions = append(desired.SchemaDefinitions, definition)
	r.databases = append(r.databases, second)
}

func (r *removalFixture) savePlan() {
	t := r.t
	candidate, refs, err := r.fx.s.RemovalCandidate(t.Context(), r.fx.app)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.fx.s.CurrentRelease(t.Context(), r.fx.app)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err := r.fx.s.LoadPlan(t.Context(), current.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range candidate.Databases {
		candidate.Databases[i] = candidate.Databases[i].WithRetention(desired.BackupRetention)
	}
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	bounds, _ := r.policy.SQLiteRemovalBounds()
	expiry := now.Add(15 * time.Minute)
	for _, ref := range refs {
		if ref.ExpiresAt.Before(expiry) {
			expiry = ref.ExpiresAt
		}
	}
	r.plan = archive.Plan{ArchiveID: data.ArchiveID(id), Requester: prepRequester, PolicyHash: r.policy.Hash(), TargetHash: "sha256:" + strings.Repeat("b", 64), Candidate: candidate, CreatedAt: now, Removal: &archive.Removal{Bounds: bounds, Points: refs, ExpiresAt: expiry}, Trees: []data.ArchiveTree{{Root: candidate.Databases[0].Reference().Root, RootDevice: 7, RootInode: 8, SourceDevice: 7, SourceInode: 4, SourceRelative: "apps/" + string(candidate.Incarnation), ArchiveRelative: "archives/" + id}}}
	r.plan.ID, err = r.plan.Fingerprint()
	if err != nil || !r.plan.Valid() {
		t.Fatal("removal plan invalid", err)
	}
	if err = r.fx.s.SaveArchivePlan(t.Context(), r.plan); err != nil {
		t.Fatal(err)
	}
}

func (r *removalFixture) createTask(kind ops.Kind, original, disposition string) ops.Operation {
	t := r.t
	in := SQLiteRetireInput{Kind: kind, App: r.fx.app, PlanID: r.plan.ID, OriginalOperationID: original, Disposition: disposition}
	input, err := r.fx.s.SaveSQLiteRetireInput(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := data.NewID()
	op, _, err := r.fx.s.CreateOperation(t.Context(), ops.Intent{Kind: kind, App: r.fx.app, SecretRef: input, RecoveryOf: original}, prepRequester, id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func (r *removalFixture) preflight() {
	if err := r.fx.s.TransitionOperation(r.t.Context(), r.task.ID, ops.Queued, ops.Preflight); err != nil {
		r.t.Fatal(err)
	}
	r.task.State = ops.Preflight
}

func (r *removalFixture) hold() {
	t := r.t
	ids := make([]data.DatabaseID, len(r.databases))
	for i, fx := range r.databases {
		ids[i] = fx.reserved.Database.DatabaseID
	}
	slices.Sort(ids)
	g, err := r.fx.s.AcquireSQLiteRemovalGuard(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	r.guard = g
	t.Cleanup(func() { _ = g.Close() })
	if err = g.BindAuthority(r.policy, r.plan.TargetHash); err != nil {
		t.Fatal(err)
	}
	for _, fx := range r.databases {
		r.attach(fx)
	}
}

func (r *removalFixture) attach(fx *prepFixture) {
	t := r.t
	child, err := r.guard.Child(fx.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := child.HoldSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	h := newPrepHostStopped(fx)
	source := &prepSource{guard: &prepGuard{host: h, guard: child}, hold: hold}
	if err = r.guard.AttachSource(t.Context(), fx.reserved.Database.DatabaseID, source); err != nil {
		t.Fatal(err)
	}
	r.hosts = append(r.hosts, h)
}

func (r *removalFixture) claim() archive.Operation {
	r.preflight()
	r.hold()
	o, err := r.fx.s.ClaimRemoval(r.t.Context(), r.plan, r.task.ID, r.guard)
	if err != nil {
		r.t.Fatal("claim removal", err)
	}
	return o
}

func (r *removalFixture) abandon(o archive.Operation) archive.RemovalReceipt {
	t := r.t
	next := archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: 1, Stage: archive.AbandonIntent}
	old, err := r.fx.s.AdvanceArchiveDisposition(t.Context(), o, archive.Disposition{}, next)
	if err != nil {
		t.Fatal(err)
	}
	next.Sequence, next.Stage = 2, archive.Abandoned
	_, receipt, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, next, r.guard)
	if err != nil {
		t.Fatal("finish removal", err)
	}
	return receipt
}

func (r *removalFixture) assertSuccess(receipt archive.RemovalReceipt) {
	t := r.t
	got, found, err := r.fx.s.ReadSQLiteRetireSuccess(t.Context(), r.task)
	if err != nil || !found || !reflect.DeepEqual(got, receipt) {
		t.Fatalf("protected readback found=%v error=%v got=%+v", found, err, got)
	}
}

// RecheckSource changes only fake physical observation. All control records and
// lifetime locks still come from the real production store.
func (r *removalFixture) changeWAL() { r.hosts[0].bundle.WAL.Inode++ }

func (r *removalFixture) preparedArchive(o archive.Operation) archive.Operation {
	t := r.t
	var err error
	for _, stage := range []archive.Stage{archive.WritersStopIntent, archive.WritersStopped, archive.CoverageIntent} {
		o, err = r.fx.s.AdvanceArchive(t.Context(), o, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	coverage := r.coverage()
	o, err = r.fx.s.SaveArchiveCoverage(t.Context(), o, coverage)
	if err != nil {
		t.Fatal("prepared coverage", err)
	}
	for _, stage := range []archive.Stage{archive.ReplicaStopIntent, archive.Quiesced} {
		o, err = r.fx.s.AdvanceArchive(t.Context(), o, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest := archive.Manifest{Plan: r.plan, OperationID: o.ID, Coverage: coverage, QuiescedAt: time.Now().UTC(), PreparedAt: time.Now().UTC()}
	o, err = r.fx.s.SaveArchiveManifest(t.Context(), o, manifest)
	if err != nil {
		t.Fatal("prepared manifest", err)
	}
	return o
}

func (r *removalFixture) coverage() []archive.Coverage {
	t := r.t
	coverage := []archive.Coverage{}
	for _, variant := range r.plan.Candidate.Databases {
		d, _ := variant.SQLite()
		point, claimed, err := r.fx.s.ReadSQLiteRemovalPoint(t.Context(), r.plan.ID, d.Binding.DatabaseID)
		if err != nil {
			t.Fatal(err)
		}
		ref, found := removalPlanRef(r.plan, d.Binding.DatabaseID)
		if !found {
			t.Fatal("missing ref")
		}
		c := archive.SQLiteCoverage{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: point.Source.Cutoff, Observation: point.Source.Point.Schema, Point: &point.Source.Point, Receipt: &point.Verification, Prepared: &archive.PreparedCoverage{Ref: ref, ClaimedAt: claimed}}
		coverage = append(coverage, archive.SQLiteCoverageVariant(c))
	}
	return coverage
}

func (r *removalFixture) retire(o archive.Operation) archive.RemovalReceipt {
	t := r.t
	old := removalReadyToFinish(t, r, o)
	next := archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: old.Sequence + 1, Stage: archive.Retired}
	_, receipt, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, next, r.guard)
	if err != nil {
		t.Fatal("retire", err)
	}
	return receipt
}

func (r *removalFixture) prepareSources() {
	if err := r.fx.s.RecordWriterAttempt(r.t.Context(), r.fx.reserved.Database.IncarnationID, "prepared-writer-attempt"); err != nil {
		r.t.Fatal(err)
	}
	for i, database := range r.databases {
		database.prepared(strings.Repeat(string(rune('1'+i)), 32))
	}
}

func (r *removalFixture) clearSourceBundle() {
	for _, database := range r.databases {
		r.originalBundles = append(r.originalBundles, database.bundle)
		database.bundle = sqliteprep.StoppedBundle{Location: database.location, DB: sqliteprep.Member{Presence: sqliteprep.Absent}, WAL: sqliteprep.Member{Presence: sqliteprep.Absent}, SHM: sqliteprep.Member{Presence: sqliteprep.Absent}, Journal: sqliteprep.Member{Presence: sqliteprep.Absent}}
	}
}
