package archive

import (
	"path"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

func archivePlan(t *testing.T) Plan {
	t.Helper()
	id := func(letter string) string { return strings.Repeat(letter, 32) }
	now := time.Now().UTC()
	destination := data.Destination{Reference: "backup", Endpoint: "https://storage.example", Region: "auto", Bucket: "test", BasePrefix: "brine", CredentialRef: "backup"}
	b := data.SQLiteDatabaseBinding{DatabaseID: data.DatabaseID(id("b")), IncarnationID: data.AppIncarnationID(id("a")), Name: "main", Root: "/private/data", RelativeDirectory: "apps/" + id("a") + "/databases/" + id("b"), MountPath: "/data", Filename: "app.sqlite", ReplicaBindingID: data.ReplicaBindingID(id("c"))}
	prefix, err := data.RemotePrefix(destination.BasePrefix, b.IncarnationID, b.DatabaseID, data.ReplicaEpochID(id("d")))
	if err != nil {
		t.Fatal(err)
	}
	allocation := data.AllocationReceipt{DatabaseID: b.DatabaseID, IncarnationID: b.IncarnationID, RootDevice: 1, RootInode: 2, DirectoryDevice: 1, DirectoryInode: 3, AllocatedAt: now.Add(-time.Hour)}
	p := Plan{ArchiveID: data.ArchiveID(id("e")), Requester: "deploy:" + strings.Repeat("a", 64), PolicyHash: "sha256:" + strings.Repeat("f", 64), TargetHash: "sha256:" + strings.Repeat("a", 64), CreatedAt: now, Candidate: Candidate{App: "hello", Incarnation: b.IncarnationID, Databases: []Database{SQLiteDatabaseVariant(SQLiteDatabase{Binding: b, Allocation: &allocation, Epochs: []data.ReplicaBinding{{BindingID: b.ReplicaBindingID, DatabaseID: b.DatabaseID, Destination: destination, EpochID: data.ReplicaEpochID(id("d")), RemotePrefix: prefix}}, Retention: []data.RetentionEvidence{{Destination: destination.Reference, Endpoint: destination.Endpoint, Bucket: destination.Bucket, BasePrefix: destination.BasePrefix, VerificationID: id("f"), VerifiedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), FreshnessSeconds: 3600, NoObjectExpiration: true}}})}}, Trees: []data.ArchiveTree{{Root: b.Root, SourceRelative: "apps/" + id("a"), ArchiveRelative: "archives/" + id("e"), RootDevice: 1, RootInode: 2, SourceDevice: 1, SourceInode: 4}}}
	p.ID, err = p.Fingerprint()
	if err != nil || !p.Valid() {
		t.Fatal("invalid fixture", err)
	}
	return p
}

func TestArchivePlanBindsRootAllocationIdentity(t *testing.T) {
	p := archivePlan(t)
	p.Candidate.Databases[0].sqlite.Allocation.RootInode++
	p.ID, _ = p.Fingerprint()
	if p.Valid() {
		t.Fatal("archive accepts data from a replaced allocation root")
	}
}

func TestArchivePlanRejectsReusedPathsAndEpochs(t *testing.T) {
	cases := map[string]func(*Plan){
		"app path":      func(p *Plan) { p.Trees[0].SourceRelative = "apps/hello" },
		"archive path":  func(p *Plan) { p.Trees[0].ArchiveRelative = "archives/hello" },
		"cross device":  func(p *Plan) { p.Trees[0].SourceDevice++ },
		"remote prefix": func(p *Plan) { p.Candidate.Databases[0].sqlite.Epochs[0].RemotePrefix = "other/" },
		"expiration":    func(p *Plan) { p.Candidate.Databases[0].sqlite.Retention[0].NoObjectExpiration = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := archivePlan(t)
			mutate(&p)
			p.ID, _ = p.Fingerprint()
			if p.Valid() {
				t.Fatal("invalid archive admitted")
			}
		})
	}
}

func TestNeverWrittenCoverageCannotPretendInitializedDataIsEmpty(t *testing.T) {
	p := archivePlan(t)
	d := p.Candidate.Databases[0]
	now := p.CreatedAt.Add(time.Second)
	fresh := *d.sqlite.Allocation
	fresh.AllocatedAt = now
	c := SQLiteCoverageVariant(SQLiteCoverage{DatabaseID: d.sqlite.Binding.DatabaseID, WritersQuiescedAt: now, Empty: &fresh, Observation: data.SQLiteSchemaObservation{DatabaseID: d.sqlite.Binding.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}})
	if !c.Admits(p, d, strings.Repeat("a", 32), now) {
		t.Fatal("affirmative never-written evidence refused")
	}
	d.sqlite.Written = true
	if c.Admits(p, d, strings.Repeat("a", 32), now) {
		t.Fatal("initialized data treated as never written")
	}
	c.sqlite.Observation.State = data.Unknown
	d.sqlite.Written = false
	if c.Admits(p, d, strings.Repeat("a", 32), now) {
		t.Fatal("unknown data admitted")
	}
}

func TestArchiveNeverWrittenEvidenceMustBeFresh(t *testing.T) {
	p := archivePlan(t)
	d := p.Candidate.Databases[0]
	now := p.CreatedAt.Add(time.Second)
	c := SQLiteCoverageVariant(SQLiteCoverage{DatabaseID: d.sqlite.Binding.DatabaseID, WritersQuiescedAt: now, Empty: d.sqlite.Allocation, Observation: data.SQLiteSchemaObservation{DatabaseID: d.sqlite.Binding.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}})
	if c.Admits(p, d, strings.Repeat("a", 32), now) {
		t.Fatal("historical empty allocation is not a fresh empty observation")
	}
}

func initializedCoverage(t *testing.T) (Plan, Database, Coverage, time.Time) {
	t.Helper()
	p := archivePlan(t)
	p.Candidate.Databases[0].sqlite.Written = true
	p.ID, _ = p.Fingerprint()
	d := p.Candidate.Databases[0]
	now := p.CreatedAt.Add(3 * time.Second)
	cutoff := p.CreatedAt.Add(time.Second)
	epoch := d.sqlite.Epochs[0]
	operation := strings.Repeat("a", 32)
	observed := data.SQLiteSchemaObservation{DatabaseID: d.sqlite.Binding.DatabaseID, State: data.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: cutoff}
	point := data.SQLiteRestorePoint{ID: strings.Repeat("f", 32), BindingID: epoch.BindingID, EpochID: epoch.EpochID, Kind: data.RestorePointSnapshot, Schema: observed, RecordedAt: cutoff.Add(time.Second), Snapshot: &data.RestoreSnapshotPoint{ObjectKey: path.Join(epoch.RemotePrefix, "restore-points", strings.Repeat("f", 32), "snapshot.sqlite"), SHA256: strings.Repeat("a", 64), Size: 4096}}
	source := restore.SQLiteSource{Kind: restore.SQLiteSnapshot, Snapshot: &restore.SnapshotSource{BindingID: string(epoch.BindingID), Epoch: string(epoch.EpochID), PointID: point.ID, ObjectKey: point.Snapshot.ObjectKey, SHA256: point.Snapshot.SHA256, Size: point.Snapshot.Size}}
	receipt := restore.SQLiteReceipt{OperationID: operation + "-" + string(d.sqlite.Binding.DatabaseID), Source: source, ToolVersion: restore.SnapshotToolVersion, ObservedAt: now, Schema: restore.SQLiteSchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "unknown", Coverage: &restore.CoverageEvidence{Source: source, CoveredThrough: cutoff}, LossWindow: restore.LossWindow{State: restore.LossBounded, From: cutoff, To: now, Reason: "Independent source-bound coverage proof bounds possible loss; later writes may be absent."}}
	c := SQLiteCoverageVariant(SQLiteCoverage{DatabaseID: d.sqlite.Binding.DatabaseID, WritersQuiescedAt: cutoff, Observation: observed, Point: &point, Receipt: &receipt})
	if !c.Admits(p, d, operation, now) {
		t.Fatal("valid remote receipt refused")
	}
	return p, d, c, now
}
func TestArchiveRemoteCoverageBindsExactSourceAndWriterCutoff(t *testing.T) {
	for name, mutate := range map[string]func(*Coverage){"wrong checksum": func(c *Coverage) { c.sqlite.Point.Snapshot.SHA256 = strings.Repeat("b", 64) }, "different cutoff": func(c *Coverage) { c.sqlite.WritersQuiescedAt = c.sqlite.WritersQuiescedAt.Add(time.Nanosecond) }, "other operation": func(c *Coverage) { c.sqlite.Receipt.OperationID = strings.Repeat("b", 32) }, "old evidence": func(c *Coverage) { c.sqlite.Receipt.ObservedAt = c.sqlite.Receipt.ObservedAt.Add(-time.Hour) }, "unchecked integrity": func(c *Coverage) { c.sqlite.Receipt.IntegrityCheck = "unknown" }, "empty exception": func(c *Coverage) { c.sqlite.Empty = &data.AllocationReceipt{} }} {
		t.Run(name, func(t *testing.T) {
			p, d, c, now := initializedCoverage(t)
			mutate(&c)
			if c.Admits(p, d, strings.Repeat("a", 32), now) {
				t.Fatal("unbound remote coverage admitted")
			}
		})
	}
	p, d, c, now := initializedCoverage(t)
	d.sqlite.Invariants = []data.RestoreInvariant{{Database: d.sqlite.Binding.Name, Kind: "row_count", Table: "commits", Count: 1}}
	if c.Admits(p, d, strings.Repeat("a", 32), now) {
		t.Fatal("declared invariants were not checked")
	}
}
