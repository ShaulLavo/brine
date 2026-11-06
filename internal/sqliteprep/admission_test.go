package sqliteprep

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

var testNow = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func id(c string) string   { return strings.Repeat(c, 32) }
func hash(c string) string { return strings.Repeat(c, 64) }
func fixtureIntent() Intent {
	scope := Scope{App: "fixture", DatabaseName: "db", IncarnationID: data.AppIncarnationID(id("a")), DatabaseID: data.DatabaseID(id("b")), BindingID: data.ReplicaBindingID(id("c")), EpochID: data.ReplicaEpochID(id("d")), ReplicaCommitted: true, ConfigSHA256: hash("a"), CredentialVersion: 2, ActivatedCredentialVersion: 2, PolicySHA256: hash("b"), TargetSHA256: hash("c"), DeclarationSHA256: hash("d"), RemotePrefix: "backup/epochs/" + id("d") + "/", Schema: data.SQLiteSchemaObservation{State: data.VerifiedSchema, DatabaseID: data.DatabaseID(id("b")), ObservedAt: testNow.Add(-time.Minute), Marker: "v1", CatalogSHA256: hash("e")}, Checks: []data.RestoreInvariant{}, Retention: Retention{EvidenceSHA256: hash("f"), ObservedAt: testNow.Add(-time.Hour), FreshUntil: testNow.Add(time.Hour), RetainUntil: testNow.Add(30 * 24 * time.Hour)}}
	input := Input{Kind: PrepareTask, PreparationID: id("e"), App: scope.App, DatabaseID: scope.DatabaseID}
	return Intent{Task: Task{OperationID: "op-fixture", Kind: PrepareTask, Requester: "owner", App: scope.App, InputSHA256: mustHash(input)}, Input: input, Scope: scope, Fence: data.QuiescenceFence{ID: data.FenceID(id("f")), IncarnationID: scope.IncarnationID, DatabaseID: scope.DatabaseID, OperationID: "original-fence-owner", State: data.FenceHeld}, Location: Location{Device: 1, Inode: 2, MountID: 3, UID: 1000, GID: 1000, Mode: 0700}, Mode: Fresh, Arm: LTX, StopWritersRequired: true, ClaimedAt: testNow}
}
func mustHash(v interface{ Validate() error }) string {
	h, err := Fingerprint(v)
	if err != nil {
		panic(err)
	}
	return h
}
func fixtureBundle() StoppedBundle {
	member := Member{Presence: Regular, Device: 1, Inode: 4, MountID: 3, UID: 1000, GID: 1000, Mode: 0600, Links: 1, Size: 4096, MtimeNS: 100, ProofSHA256: hash("a")}
	wal := member
	wal.Inode = 5
	return StoppedBundle{Location: fixtureIntent().Location, DB: member, WAL: wal, SHM: Member{Presence: Absent}, Journal: Member{Presence: Absent}}
}
func fixtureObservation() Observation {
	return Observation{Replica: Stopped, WritersStopped: true, JobsAbsent: true, Owned: true, Schema: fixtureIntent().Scope.Schema}
}
func fixtureSource(i Intent) RemoteSource {
	return RemoteSource{Origin: i.Input.PreparationID, Point: data.SQLiteRestorePoint{ID: i.Input.PreparationID, BindingID: i.Scope.BindingID, EpochID: i.Scope.EpochID, Kind: data.RestorePointLTX, Schema: i.Scope.Schema, LTX: &data.RestoreLTXPoint{TXID: 20, Barrier: data.RestoreUploadBarrier{BindingID: i.Scope.BindingID, EpochID: i.Scope.EpochID, TXID: 20, ReplicaTXID: 21, ObservedAt: testNow.Add(time.Second)}}, RecordedAt: testNow.Add(2 * time.Second)}, Cutoff: testNow, UploadedAt: testNow.Add(2 * time.Second)}
}
func fixtureLineage() Lineage {
	i := fixtureIntent()
	b := fixtureBundle()
	src := fixtureSource(i)
	return Lineage{Previous: i, Terminal: true, Unconsumed: true, Baseline: &StoppedRecord{PreparationID: i.Input.PreparationID, Scope: i.Scope, Fence: i.Fence, Bundle: b}, Coverage: &CoverageRecord{Source: src, Scope: i.Scope, Fence: i.Fence, BaselinePreparationID: i.Input.PreparationID, BaselineSHA256: mustHash(b)}, OwnStopped: true, OwnUploaded: true}
}

func TestAdmissionR1ModesAndBoundaries(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Lineage, *Observation, *StoppedBundle)
		mode Mode
		code Code
	}{
		{name: "1 completed LTX reverify", mode: SupersedeReverify},
		{name: "2 lost stop no baseline captures", edit: func(l *Lineage, _ *Observation, _ *StoppedBundle) {
			l.Baseline = nil
			l.Coverage = nil
			l.OwnStopped = false
			l.OwnUploaded = false
		}, mode: SupersedeCapture},
		{name: "2 oversized no coverage", edit: func(l *Lineage, _ *Observation, b *StoppedBundle) {
			l.Baseline = nil
			l.Coverage = nil
			l.OwnStopped = false
			l.OwnUploaded = false
			b.DB.Size = MaxCaptureBytes + 1
		}, code: CoverageUnavailable},
		{name: "3 replaced WAL same database", edit: func(_ *Lineage, _ *Observation, b *StoppedBundle) { b.WAL.Inode++ }, code: StoppedSourceChanged},
		{name: "3 safe SHM rebuild", edit: func(_ *Lineage, _ *Observation, b *StoppedBundle) {
			b.SHM = b.WAL
			b.SHM.Inode = 99
			b.SHM.ProofSHA256 = ""
		}, mode: SupersedeReverify},
		{name: "4 live never baselined", edit: func(l *Lineage, o *Observation, _ *StoppedBundle) {
			l.Baseline = nil
			l.Coverage = nil
			l.OwnStopped = false
			l.OwnUploaded = false
			o.Replica = Running
		}, mode: SupersedeLive},
		{name: "4 running after baseline", edit: func(_ *Lineage, o *Observation, _ *StoppedBundle) { o.Replica = Running }, code: StoppedSourceChanged},
		{name: "5 matching baseline missing upload", edit: func(l *Lineage, _ *Observation, _ *StoppedBundle) { l.Coverage = nil; l.OwnUploaded = false }, mode: SupersedeCapture},
		{name: "5 changed WAL missing upload", edit: func(l *Lineage, _ *Observation, b *StoppedBundle) {
			l.Coverage = nil
			l.OwnUploaded = false
			b.WAL.ProofSHA256 = hash("b")
		}, code: StoppedSourceChanged},
		{name: "8 pending job", edit: func(_ *Lineage, o *Observation, _ *StoppedBundle) { o.JobsAbsent = false }, code: LocalUncertain},
		{name: "8 foreign owner", edit: func(_ *Lineage, o *Observation, _ *StoppedBundle) { o.Owned = false }, code: LocalUncertain},
		{name: "8 unknown schema", edit: func(_ *Lineage, o *Observation, _ *StoppedBundle) { o.Schema.State = data.Unknown }, code: LocalUncertain},
		{name: "unproved coverage never reused", edit: func(l *Lineage, _ *Observation, _ *StoppedBundle) { l.Coverage = nil }, mode: SupersedeCapture},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkAdmissionCase(t, tc.edit, tc.mode, tc.code)
		})
	}
}
func checkSelection(t *testing.T, s Selection, err error, mode Mode, code Code) {
	t.Helper()
	if code != "" {
		var refused *Refusal
		if !errors.As(err, &refused) || refused.Code != code {
			t.Fatalf("got %v, want %s", err, code)
		}
		return
	}
	if err != nil || s.Mode != mode {
		t.Fatalf("got %+v, %v; want %s", s, err, mode)
	}
}
func TestInheritedBaselineAndCoverageSurviveClaimCrash(t *testing.T) {
	for _, stage := range []Stage{Claimed, WritersStopped, ReplicaStopped} {
		t.Run(string(stage), func(t *testing.T) {
			checkInheritedCrash(t, stage)
		})
	}
}
func ptrBundle(b StoppedBundle) *StoppedBundle { return &b }
func TestInheritedCaptureBaselineCannotBeForgotten(t *testing.T) {
	l := fixtureLineage()
	origin := l.Previous.Input.PreparationID
	l.Previous.Mode = SupersedeCapture
	l.Previous.Arm = Snapshot
	l.Previous.Input.Supersedes = origin
	l.Previous.Input.PreparationID = id("1")
	l.Previous.BaselineFrom = origin
	l.Previous.Task.InputSHA256 = mustHash(l.Previous.Input)
	l.OwnStopped = false
	l.OwnUploaded = false
	l.Coverage = nil
	s, err := Select(l.Previous.Scope, &l, fixtureObservation(), ptrBundle(fixtureBundle()), testNow.Add(time.Minute))
	checkSelection(t, s, err, SupersedeCapture, "")
	if s.BaselineFrom != origin || s.CoverageOrigin != "" {
		t.Fatal(s)
	}
	b := fixtureBundle()
	b.WAL.Inode++
	_, err = Select(l.Previous.Scope, &l, fixtureObservation(), &b, testNow.Add(time.Minute))
	checkSelection(t, Selection{}, err, "", StoppedSourceChanged)
}
func TestOriginsRefuseCrossedEvidence(t *testing.T) {
	edits := []func(*Lineage){func(l *Lineage) { l.Coverage.Source.Origin = id("9") }, func(l *Lineage) { l.Coverage.BaselineSHA256 = hash("9") }, func(l *Lineage) { l.Baseline.Fence.OperationID = "replacement-owner" }, func(l *Lineage) { l.Baseline.Scope.EpochID = data.ReplicaEpochID(id("9")) }, func(l *Lineage) { l.Coverage.Fence.OperationID = "replacement-owner" }, func(l *Lineage) { l.Previous.BaselineFrom = id("9"); l.OwnStopped = false }, func(l *Lineage) { l.Unconsumed = false }, func(l *Lineage) { l.Terminal = false }}
	for n, edit := range edits {
		l := fixtureLineage()
		edit(&l)
		if _, err := ResolveOrigins(l); err == nil {
			t.Fatalf("case %d accepted", n)
		}
	}
}
func TestModeSpecificEdges(t *testing.T) {
	cases := []struct {
		mode Mode
		arm  Arm
		path []Stage
	}{
		{Fresh, LTX, []Stage{Claimed, WritersStopIntent, WritersStopped, CoverageUploaded, Verified, ReplicaStopIntent, ReplicaStopped, Prepared}},
		{Fresh, Snapshot, []Stage{Claimed, WritersStopIntent, WritersStopped, ReplicaStopped, CoverageIntent, CoverageUploaded, Verified, Prepared}},
		{Fresh, Empty, []Stage{Claimed, WritersStopIntent, WritersStopped, ReplicaStopped, CoverageIntent, CoverageUploaded, Verified, Prepared}},
		{SupersedeLive, LTX, []Stage{Claimed, WritersStopped, CoverageUploaded, Verified, ReplicaStopIntent, ReplicaStopped, Prepared}},
		{SupersedeReverify, LTX, []Stage{Claimed, WritersStopped, ReplicaStopped, CoverageRetained, Verified, Prepared}},
		{SupersedeReverify, Snapshot, []Stage{Claimed, WritersStopped, ReplicaStopped, CoverageRetained, Verified, Prepared}},
		{SupersedeCapture, Snapshot, []Stage{Claimed, WritersStopped, ReplicaStopped, CoverageIntent, CoverageUploaded, Verified, Prepared}},
	}
	for _, tc := range cases {
		checkEdges(t, tc.mode, tc.arm, tc.path)
	}
	for _, edge := range [][4]string{{string(Fresh), string(Snapshot), string(Verified), string(ReplicaStopIntent)}, {string(SupersedeReverify), string(LTX), string(ReplicaStopped), string(CoverageUploaded)}, {string(SupersedeCapture), string(LTX), string(Claimed), string(WritersStopped)}, {"unknown", string(LTX), string(Claimed), string(WritersStopped)}} {
		if Next(Mode(edge[0]), Arm(edge[1]), Stage(edge[2]), Stage(edge[3])) {
			t.Fatal("accepted bad edge", edge)
		}
	}
}
func TestStoppedBaselineCannotHaveRunningWriters(t *testing.T) {
	l := fixtureLineage()
	o := fixtureObservation()
	o.WritersStopped = false
	s, err := Select(l.Previous.Scope, &l, o, ptrBundle(fixtureBundle()), testNow.Add(time.Minute))
	checkSelection(t, s, err, "", StoppedSourceChanged)
}
func TestSelectCannotReverifyWhileCredentialVersionOrFenceChanges(t *testing.T) {
	l := fixtureLineage()
	s := l.Previous.Scope
	s.CredentialVersion++
	if _, err := Select(s, &l, fixtureObservation(), ptrBundle(fixtureBundle()), testNow.Add(time.Minute)); err == nil {
		t.Fatal("changed scope accepted")
	}
}

func checkAdmissionCase(t *testing.T, edit func(*Lineage, *Observation, *StoppedBundle), mode Mode, code Code) {
	t.Helper()
	l := fixtureLineage()
	o := fixtureObservation()
	b := fixtureBundle()
	if edit != nil {
		edit(&l, &o, &b)
	}
	selection, err := Select(l.Previous.Scope, &l, o, &b, testNow.Add(time.Minute))
	checkSelection(t, selection, err, mode, code)
}

func checkInheritedCrash(t *testing.T, stage Stage) {
	t.Helper()
	l := fixtureLineage()
	origin := l.Previous.Input.PreparationID
	l.Previous.Mode = SupersedeReverify
	l.Previous.Input.Supersedes = origin
	l.Previous.Input.PreparationID = id("1")
	l.Previous.Task.InputSHA256 = mustHash(l.Previous.Input)
	l.Previous.BaselineFrom = origin
	l.Previous.CoverageOrigin = origin
	l.Previous.StopWritersRequired = false
	l.OwnStopped = false
	l.OwnUploaded = false
	history := History{Intent: l.Previous, Events: []Event{{PreparationID: l.Previous.Input.PreparationID, Sequence: 1, Stage: Claimed, At: l.Previous.ClaimedAt}}}
	if stage != Claimed {
		history.Events = append(history.Events, Event{PreparationID: l.Previous.Input.PreparationID, Sequence: 2, Stage: WritersStopped, At: testNow.Add(time.Second)})
	}
	baselineID := origin
	if stage == ReplicaStopped {
		bundle := fixtureBundle()
		history.Events = append(history.Events, Event{PreparationID: l.Previous.Input.PreparationID, Sequence: 3, Stage: ReplicaStopped, At: testNow.Add(2 * time.Second), Stopped: &bundle})
		l.OwnStopped = true
		baselineID = l.Previous.Input.PreparationID
		l.Baseline.PreparationID = baselineID
		l.Coverage.BaselinePreparationID = baselineID
	}
	if err := history.Validate(); err != nil {
		t.Fatal("invalid interrupted journal", err)
	}
	s, err := Select(l.Previous.Scope, &l, fixtureObservation(), ptrBundle(fixtureBundle()), testNow.Add(time.Minute))
	checkSelection(t, s, err, SupersedeReverify, "")
	if s.BaselineFrom != baselineID || s.CoverageOrigin != origin || s.Source.Origin != origin {
		t.Fatalf("relabelled origins: %+v", s)
	}
	b := fixtureBundle()
	b.WAL.Inode++
	_, err = Select(l.Previous.Scope, &l, fixtureObservation(), &b, testNow.Add(time.Minute))
	checkSelection(t, Selection{}, err, "", StoppedSourceChanged)
}

func checkEdges(t *testing.T, mode Mode, arm Arm, path []Stage) {
	t.Helper()
	for n := 1; n < len(path); n++ {
		if !Next(mode, arm, path[n-1], path[n]) {
			t.Fatalf("refused %s/%s %s -> %s", mode, arm, path[n-1], path[n])
		}
	}
}
