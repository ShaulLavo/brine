package sqliteprep

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func fixturePrepared(mode Mode, arm Arm) (History, PreparedPoint) {
	i := fixtureIntent()
	i.Mode = mode
	i.Arm = arm
	if mode != Fresh {
		i.Input.Supersedes = id("9")
		i.Task.InputSHA256 = mustHash(i.Input)
		i.StopWritersRequired = false
	}
	if mode == SupersedeReverify {
		i.BaselineFrom = id("9")
		i.CoverageOrigin = id("9")
	}
	if arm == Empty {
		i.Scope.ReplicaCommitted = false
		i.Scope.ActivatedCredentialVersion = 0
		i.Scope.Schema.State = data.AllocatedEmpty
		i.Scope.Schema.Marker = data.EmptyMarker
		i.Scope.Schema.CatalogSHA256 = data.EmptyCatalogSHA256
		i.Scope.EmptyAllocation = &EmptyAllocation{AllocationSHA256: hash("1"), NeverWrittenSHA256: hash("2"), DatabaseID: i.Scope.DatabaseID, IncarnationID: i.Scope.IncarnationID, Location: i.Location}
	}
	h := History{Intent: i, Events: []Event{}}
	at := testNow
	emit := func(e Event) {
		e.PreparationID = i.Input.PreparationID
		e.Sequence = len(h.Events) + 1
		e.At = at
		h.Events = append(h.Events, e)
		at = at.Add(time.Second)
	}
	emit(Event{Stage: Claimed})
	if i.StopWritersRequired {
		emit(Event{Stage: WritersStopIntent})
	}
	cutoff := at
	emit(Event{Stage: WritersStopped})
	b := fixtureBundle()
	if arm == Empty {
		b.DB = Member{Presence: Absent}
		b.WAL = Member{Presence: Absent}
	}
	src := fixtureSource(i)
	src.Cutoff = cutoff
	if mode == SupersedeReverify {
		src.Origin = id("9")
		src.Point.ID = src.Origin
		src.Cutoff = testNow.Add(-time.Minute)
		src.UploadedAt = testNow.Add(-time.Second)
		src.Point.RecordedAt = src.UploadedAt
		src.Point.LTX.Barrier.ObservedAt = src.UploadedAt
	}
	if arm != LTX {
		src.Point.Kind = data.RestorePointSnapshot
		src.Point.LTX = nil
		src.Point.Schema = pointSchema(i.Scope.Schema)
		src.Point.Snapshot = &data.RestoreSnapshotPoint{ObjectKey: SnapshotKey(i.Scope, src.Origin), SHA256: hash("3"), Size: 4096}
	}
	running := arm == LTX && mode != SupersedeReverify
	if !running {
		emit(Event{Stage: ReplicaStopped, Stopped: &b})
	}
	switch {
	case mode == SupersedeReverify:
		emit(Event{Stage: CoverageRetained, Source: &src})
	case arm != LTX:
		p := SnapshotPlan{Point: src.Point, Cutoff: cutoff}
		p.Point.RecordedAt = at
		emit(Event{Stage: CoverageIntent, Snapshot: &p})
		src.UploadedAt = at
		src.Point.RecordedAt = at
		emit(Event{Stage: CoverageUploaded, Source: &src})
	default:
		src.UploadedAt = at
		src.Point.RecordedAt = at
		src.Point.LTX.Barrier.ObservedAt = at
		emit(Event{Stage: CoverageUploaded, Source: &src})
	}
	verification := receiptFor(i, src, at)
	emit(Event{Stage: Verified, Verification: &verification})
	if running {
		emit(Event{Stage: ReplicaStopIntent})
		emit(Event{Stage: ReplicaStopped, Stopped: &b})
	}
	p := PreparedPoint{PreparationID: i.Input.PreparationID, IntentSHA256: mustHash(i), Source: src, Verification: verification, Stopped: b, PreparedAt: at, ExpiresAt: at.Add(PointLifetime)}
	return h, p
}
func TestCanonicalCodecsClosedAtEveryDepth(t *testing.T) {
	h, p := fixturePrepared(Fresh, LTX)
	if err := ValidatePrepared(h, p); err != nil {
		t.Fatal("fixture", err)
	}
	raw, err := Encode(h.Intent)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeIntent(raw)
	if err != nil || mustHash(decoded) != mustHash(h.Intent) {
		t.Fatal(err)
	}
	edits := [][]byte{
		bytes.Replace(raw, []byte(`"mode":"fresh"`), []byte(`"mode":"fresh","mode":"fresh"`), 1),
		bytes.Replace(raw, []byte(`"mode":"fresh"`), []byte(`"Mode":"fresh"`), 1),
		bytes.Replace(raw, []byte(`"mode":"fresh"`), []byte(`"mode":"fresh","extra":true`), 1),
		bytes.Replace(raw, []byte(`"checks":[]`), []byte(`"checks":null`), 1),
		bytes.Replace(raw, []byte(`"device":1`), []byte(`"device":1,"hidden":0`), 1),
		append(append([]byte{}, raw...), []byte(`{}`)...),
		append([]byte(" "), raw...),
	}
	for n, bad := range edits {
		if _, err := DecodeIntent(bad); err == nil {
			t.Fatalf("accepted malformed %d", n)
		}
	}
	pointRaw, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePreparedPoint(pointRaw); err != nil {
		t.Fatal(err)
	}
	crossed := bytes.Replace(pointRaw, []byte(`"integrity_check":"passed"`), []byte(`"integrity_check":"passed","integrity_check":"passed"`), 1)
	if _, err := DecodePreparedPoint(crossed); err == nil {
		t.Fatal("nested duplicate restore receipt accepted")
	}
	if _, err := DecodeIntent(bytes.Repeat([]byte(" "), MaxRecordBytes+1)); !errors.Is(err, ErrLimit) {
		t.Fatal("unbounded decode", err)
	}
}
func TestPreparedValidationUsesWholeModePath(t *testing.T) {
	for _, pair := range [][2]string{{string(Fresh), string(LTX)}, {string(Fresh), string(Snapshot)}, {string(Fresh), string(Empty)}, {string(SupersedeLive), string(LTX)}, {string(SupersedeReverify), string(LTX)}, {string(SupersedeReverify), string(Snapshot)}, {string(SupersedeCapture), string(Snapshot)}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			checkPreparedPath(t, pair)
		})
	}
}
func TestPreparedNegativeSourceReceiptAndStageBindings(t *testing.T) {
	edits := []func(*History, *PreparedPoint){
		func(_ *History, p *PreparedPoint) { p.Verification.OperationID = "foreign" },
		func(_ *History, p *PreparedPoint) { p.Source.Cutoff = p.Source.Cutoff.Add(-time.Second) },
		func(_ *History, p *PreparedPoint) { p.Verification.RequestedTXID++ },
		func(_ *History, p *PreparedPoint) { p.Verification.RecoveredTXID++ },
		func(_ *History, p *PreparedPoint) { p.Verification.Schema.CatalogSHA256 = hash("9") },
		func(_ *History, p *PreparedPoint) { p.Verification.InvariantCheck = "passed" },
		func(_ *History, p *PreparedPoint) { p.Verification.DatabasePath = "/private/workspace" },
		func(_ *History, p *PreparedPoint) { p.Stopped.WAL.Inode++ },
		func(h *History, _ *PreparedPoint) { h.Intent.StopWritersRequired = false },
		func(h *History, _ *PreparedPoint) { h.Events[2].Sequence++ },
		func(h *History, _ *PreparedPoint) { h.Events[3].Stage = CoverageRetained },
		func(h *History, _ *PreparedPoint) { h.Events[3].At = testNow.Add(-time.Second) },
		func(h *History, _ *PreparedPoint) { h.Intent.Fence.OperationID = "new-fence-owner" },
		func(h *History, _ *PreparedPoint) { h.Intent.Scope.DeclarationSHA256 = hash("9") },
	}
	for n, edit := range edits {
		h, p := fixturePrepared(Fresh, LTX)
		edit(&h, &p)
		if ValidatePrepared(h, p) == nil {
			t.Fatalf("accepted negative case %d", n)
		}
	}
}
func TestAllocatedEmptyRequiresSeparateProtectedAllocation(t *testing.T) {
	h, p := fixturePrepared(Fresh, Empty)
	if err := ValidatePrepared(h, p); err != nil {
		t.Fatal(err)
	}
	h.Intent.Scope.EmptyAllocation = nil
	if h.Intent.Validate() == nil {
		t.Fatal("schema alone replaced empty allocation proof")
	}
}
func TestMemberSafetyAndAuthoritativePresence(t *testing.T) {
	edits := []func(*StoppedBundle){func(b *StoppedBundle) { b.WAL.Links = 2 }, func(b *StoppedBundle) { b.SHM = Member{Presence: "unknown"} }, func(b *StoppedBundle) { b.DB.Mode = 0644 }, func(b *StoppedBundle) { b.Journal = Member{Presence: Absent, Size: 1} }, func(b *StoppedBundle) { b.DB.Device++ }, func(b *StoppedBundle) { b.WAL.ProofSHA256 = "" }, func(b *StoppedBundle) { b.DB.Presence = Absent }, func(b *StoppedBundle) { b.SHM = b.DB }}
	for n, edit := range edits {
		b := fixtureBundle()
		edit(&b)
		if b.Validate() == nil {
			t.Fatalf("unsafe member %d accepted", n)
		}
	}
	b := fixtureBundle()
	other := b
	other.WAL = Member{Presence: Absent}
	if b.Matches(other) {
		t.Fatal("WAL disappearance accepted")
	}
	b.SHM = b.WAL
	b.SHM.ProofSHA256 = ""
	other = b
	other.SHM.Inode++
	other.SHM.Size++
	other.SHM.MtimeNS++
	if !b.Matches(other) {
		t.Fatal("safe SHM rebuild rejected")
	}
	other.SHM = Member{Presence: Absent}
	if !b.Matches(other) {
		t.Fatal("safe SHM disappearance rejected")
	}
}
func TestFreshLTXCannotUseUncommittedReplica(t *testing.T) {
	i := fixtureIntent()
	i.Scope.ReplicaCommitted = false
	i.Scope.ActivatedCredentialVersion = 0
	if i.Validate() == nil {
		t.Fatal("fresh LTX granted on uncommitted binding")
	}
}
func TestConsumerFreshnessCannotRefreshBackupThroughReverify(t *testing.T) {
	h, p := fixturePrepared(SupersedeReverify, LTX)
	now := p.PreparedAt
	c := Consumer{PreparationID: p.PreparationID, OperationID: "consumer", Requester: h.Intent.Task.Requester, App: h.Intent.Scope.App, InputSHA256: hash("1"), Kind: ReviewedConsumer, ReviewedPlanSHA256: hash("2"), PreparedSHA256: mustHash(p), PolicySHA256: h.Intent.Scope.PolicySHA256, RetentionSHA256: h.Intent.Scope.Retention.EvidenceSHA256, Bounds: &data.InitializationBounds{MaxBackupAgeSeconds: 120, MaxRestoreTestAgeSeconds: 30, RecoveryWindowSeconds: 3600}, ClaimedAt: now, RecoverUntil: now.Add(time.Hour)}
	if !c.Admits(h.Intent, p, h.Intent.Scope, now) {
		raw, _ := json.Marshal(c)
		t.Fatalf("valid consumer rejected: %s", raw)
	}
	c.Bounds.MaxBackupAgeSeconds = 1
	if c.Admits(h.Intent, p, h.Intent.Scope, now) {
		t.Fatal("fresh verification refreshed old backup age")
	}
	c.Bounds.MaxBackupAgeSeconds = 120
	c.Bounds.RecoveryWindowSeconds = 0
	if c.Admits(h.Intent, p, h.Intent.Scope, now) {
		t.Fatal("zero recovery window accepted")
	}
}

func TestSnapshotIntentBindsScopeAndCutoffBeforeUpload(t *testing.T) {
	for _, edit := range []func(*History){
		func(h *History) { h.Events[4].Snapshot.Point.BindingID = data.ReplicaBindingID(id("9")) },
		func(h *History) { h.Events[4].Snapshot.Cutoff = testNow },
		func(h *History) { h.Events[4].Snapshot.Point.Snapshot.ObjectKey = "foreign/snapshot.sqlite" },
		func(h *History) { h.Events[4].Snapshot.Point.Schema.Marker = "foreign" },
	} {
		h, _ := fixturePrepared(Fresh, Snapshot)
		edit(&h)
		h.Events = h.Events[:5]
		if h.Validate() == nil {
			t.Fatal("crossed snapshot intent accepted")
		}
	}
}
func TestCompleteJournalAndReceiptValidateTogether(t *testing.T) {
	h, p := fixturePrepared(Fresh, Snapshot)
	digest := mustHash(p)
	r := Receipt{Task: h.Intent.Task, PreparationID: p.PreparationID, PreparedSHA256: digest, Fence: h.Intent.Fence, SourcePointID: p.Source.Point.ID, Outcome: PreparedOutcome, CompletedAt: p.PreparedAt}
	h.Events = append(h.Events, Event{PreparationID: p.PreparationID, Sequence: len(h.Events) + 1, Stage: Prepared, At: p.PreparedAt, PreparedSHA256: digest})
	if err := ValidateCompletion(h, p, r); err != nil {
		t.Fatal(err)
	}
	r.Fence.OperationID = "successor-owner"
	if ValidateCompletion(h, p, r) == nil {
		t.Fatal("fence owner changed on read")
	}
}
func TestConsumerBindsExactPreparedBytesAndOriginalCoverageAge(t *testing.T) {
	h, p := fixturePrepared(SupersedeReverify, LTX)
	now := p.PreparedAt
	c := Consumer{PreparationID: p.PreparationID, OperationID: "consumer", Requester: h.Intent.Task.Requester, App: h.Intent.Scope.App, InputSHA256: hash("1"), Kind: ReviewedConsumer, ReviewedPlanSHA256: hash("2"), PreparedSHA256: mustHash(p), PolicySHA256: h.Intent.Scope.PolicySHA256, RetentionSHA256: h.Intent.Scope.Retention.EvidenceSHA256, Bounds: &data.InitializationBounds{MaxBackupAgeSeconds: 120, MaxRestoreTestAgeSeconds: 30, RecoveryWindowSeconds: 3600}, ClaimedAt: now, RecoverUntil: now.Add(time.Hour)}
	if !c.Admits(h.Intent, p, h.Intent.Scope, now) {
		t.Fatal("valid exact consumer rejected")
	}
	c.PreparedSHA256 = hash("9")
	if c.Admits(h.Intent, p, h.Intent.Scope, now) {
		t.Fatal("different point bytes accepted")
	}
	c.PreparedSHA256 = mustHash(p)
	c.Bounds.MaxBackupAgeSeconds = 30
	if c.Admits(h.Intent, p, h.Intent.Scope, now) {
		t.Fatal("source cutoff was younger than its original coverage")
	}
}

func TestPointDecoderRejectsCrossedSchemaAndCoverage(t *testing.T) {
	_, p := fixturePrepared(Fresh, LTX)
	p.Verification.Schema.CatalogSHA256 = hash("9")
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePreparedPoint(raw); err == nil {
		t.Fatal("point decoder accepted crossed verified schema")
	}
	_, p = fixturePrepared(Fresh, LTX)
	p.Verification.Coverage.CoveredThrough = p.Verification.Coverage.CoveredThrough.Add(-time.Second)
	p.Verification.LossWindow.From = p.Verification.Coverage.CoveredThrough
	raw, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePreparedPoint(raw); err == nil {
		t.Fatal("point decoder accepted moved coverage cutoff")
	}
}
func TestDeliveredCredentialVersionDoesNotActivateReservedReplica(t *testing.T) {
	i := fixtureIntent()
	i.Scope.ReplicaCommitted = false
	i.Scope.ActivatedCredentialVersion = 0
	i.Arm = Snapshot
	if err := i.Validate(); err != nil {
		t.Fatal("delivered nonzero version refused for reserved replica", err)
	}
	i.Scope.CredentialVersion = 0
	if i.Validate() == nil {
		t.Fatal("version zero selected as delivered credential")
	}
}

func checkPreparedPath(t *testing.T, pair [2]string) {
	t.Helper()
	h, p := fixturePrepared(Mode(pair[0]), Arm(pair[1]))
	if err := ValidatePrepared(h, p); err != nil {
		t.Fatal(err)
	}
	h.Events = h.Events[:len(h.Events)-1]
	if ValidatePrepared(h, p) == nil {
		t.Fatal("incomplete path accepted")
	}
}

func TestClosedRecordRoundTrips(t *testing.T) {
	h, p := fixturePrepared(Fresh, LTX)
	receipt := Receipt{Task: h.Intent.Task, PreparationID: p.PreparationID, PreparedSHA256: mustHash(p), Fence: h.Intent.Fence, SourcePointID: p.Source.Point.ID, Outcome: PreparedOutcome, CompletedAt: p.PreparedAt}
	consumer := Consumer{PreparationID: p.PreparationID, OperationID: "successor", Requester: h.Intent.Task.Requester, App: h.Intent.Task.App, InputSHA256: hash("9"), Kind: SupersedeConsumer, SuccessorID: id("9"), PolicySHA256: h.Intent.Scope.PolicySHA256, ClaimedAt: p.PreparedAt}
	roundTrip(t, h.Intent.Input, func(raw []byte) (record, error) { return DecodeInput(raw) })
	roundTrip(t, h.Events[0], func(raw []byte) (record, error) { return DecodeEvent(raw) })
	roundTrip(t, receipt, func(raw []byte) (record, error) { return DecodeReceipt(raw) })
	roundTrip(t, consumer, func(raw []byte) (record, error) { return DecodeConsumer(raw) })
	raw, _ := Encode(receipt)
	raw = bytes.Replace(raw, []byte(`"operation_id":"op-fixture"`), []byte(`"operation_id":"op-fixture","unknown":"secret"`), 1)
	if _, err := DecodeReceipt(raw); err == nil {
		t.Fatal("open receipt task object")
	}
}
func roundTrip(t *testing.T, v record, decodeRecord func([]byte) (record, error)) {
	t.Helper()
	raw, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(decoded)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("noncanonical round trip", err)
	}
	if _, err := decodeRecord(append(raw, []byte(`{}`)...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
func TestCounterValidationDoesNotUseReplicaHeadAsRecoveredPosition(t *testing.T) {
	h, p := fixturePrepared(Fresh, LTX)
	if p.Source.Point.LTX.TXID == p.Source.Point.LTX.Barrier.ReplicaTXID {
		t.Fatal("fixture needs a replica head ahead of the exact target")
	}
	p.Verification.RecoveredTXID = p.Source.Point.LTX.Barrier.ReplicaTXID
	if ValidatePrepared(h, p) == nil {
		t.Fatal("replica head substituted for exact recovered target")
	}
}

func TestEventRejectsNegativeSequenceAndCrossedSourceArm(t *testing.T) {
	e := Event{PreparationID: id("e"), Sequence: -1, Stage: Claimed, At: testNow}
	if e.Validate() == nil {
		t.Fatal("negative sequence accepted")
	}
	h, p := fixturePrepared(SupersedeReverify, LTX)
	h.Intent.Arm = Snapshot
	p.IntentSHA256 = mustHash(h.Intent)
	if ValidatePrepared(h, p) == nil {
		t.Fatal("LTX retained under a snapshot arm")
	}
}
