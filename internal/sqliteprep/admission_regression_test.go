package sqliteprep

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestSuccessorCurrentAuthorization(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Facts)
	}{
		{"unchanged_control", func(*Facts) {}},
		{"schema_observation_time", func(f *Facts) { f.Scope.Schema.ObservedAt = testNow.Add(30 * time.Second) }},
		{"renewed_delivered_credential", func(f *Facts) { f.Scope.CredentialVersion++ }},
		{"renewed_retention", func(f *Facts) {
			f.Scope.Retention.EvidenceSHA256 = hash("1")
			f.Scope.Retention.ObservedAt = testNow
			f.Scope.Retention.FreshUntil = testNow.Add(2 * time.Hour)
			f.Scope.Retention.RetainUntil = testNow.Add(31 * 24 * time.Hour)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertSuccessorAuthorization(t, tc.edit) })
	}
}
func assertSuccessorAuthorization(t *testing.T, edit func(*Facts)) {
	t.Helper()
	s, j, h, task, in := supersedeFixture()
	origin := j.facts.Lineage.Coverage.Source
	edit(&j.facts)
	h.obs.Schema = j.facts.Scope.Schema
	r, err := s.Prepare(t.Context(), task, in)
	if err != nil || r.Receipt.Outcome != PreparedOutcome {
		t.Fatalf("identical semantic source refused: %v; state=%s host=%v journal=%v", err, j.status.State, h.calls, j.calls)
	}
	if !reflect.DeepEqual(j.point.Source, origin) || slices.Contains(h.calls, "upload") || slices.Contains(h.calls, "barrier") {
		t.Fatal("source identity changed or new upload/barrier", h.calls)
	}
}
func TestAbandonRenewedCredentialAndExpiredRemote(t *testing.T) {
	s, j, h, task, in := supersedeFixture()
	in.Kind = AbandonTask
	in.Supersedes = ""
	in.PreparationID = j.facts.Lineage.Previous.Input.PreparationID
	task.Kind = AbandonTask
	task.InputSHA256 = mustHash(in)
	j.facts.Scope.CredentialVersion++
	j.facts.CredentialExpiresAt = testNow
	r, err := s.Abandon(t.Context(), task, in)
	if err != nil || r.Receipt.Outcome != AbandonedOutcome {
		t.Fatalf("safe local abandon stranded by remote renewal: %v; state=%s host=%v", err, j.status.State, h.calls)
	}
}
func TestSemanticScopeChangesStillRefuse(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Facts)
	}{
		{"binding", func(f *Facts) { f.Scope.BindingID = data.ReplicaBindingID(id("9")) }},
		{"configuration", func(f *Facts) { f.Scope.ConfigSHA256 = hash("9") }},
		{"destination_prefix", func(f *Facts) { f.Scope.RemotePrefix = "different/" }},
		{"declaration", func(f *Facts) { f.Scope.DeclarationSHA256 = hash("9") }},
		{"schema", func(f *Facts) { f.Scope.Schema.Marker = "changed" }},
		{"shortened_retention", func(f *Facts) { f.Scope.Retention.RetainUntil = testNow.Add(24 * time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertChangedSourceScope(t, tc.edit) })
	}
}
func assertChangedSourceScope(t *testing.T, edit func(*Facts)) {
	t.Helper()
	s, j, h, task, in := supersedeFixture()
	edit(&j.facts)
	h.obs.Schema = j.facts.Scope.Schema
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || slices.Contains(j.calls, "claim") || len(h.calls) != 0 {
		t.Fatal("changed source scope claimed", err, j.calls, h.calls)
	}
}

type noClaimCompletionJournal struct{ *fakeJournal }

func (j noClaimCompletionJournal) CompleteAbandon(context.Context, Task, Input, Abandonment, StoppedSource) (Receipt, error) {
	return Receipt{}, refuse(LocalUncertain)
}
func TestAbandonNoClaimRefusalIsFailed(t *testing.T) {
	s, j, _, task, in := supersedeFixture()
	in.Kind = AbandonTask
	in.Supersedes = ""
	in.PreparationID = j.facts.Lineage.Previous.Input.PreparationID
	task.Kind = AbandonTask
	task.InputSHA256 = mustHash(in)
	s.Journal = noClaimCompletionJournal{j}
	_, err := s.Abandon(t.Context(), task, in)
	if err == nil || j.status.Claimed || j.status.Receipt != nil {
		t.Fatal("fixture must give a typed refusal with a protected no-claim readback", err, j.status)
	}
	if j.status.State != Failed {
		t.Fatalf("typed no-effect refusal became %s despite Claimed=%t", j.status.State, j.status.Claimed)
	}
}
func TestCaptureAdmissionCountsExistingSHMBudget(t *testing.T) {
	l := fixtureLineage()
	l.Baseline = nil
	l.Coverage = nil
	l.OwnStopped = false
	l.OwnUploaded = false
	b := fixtureBundle()
	b.DB.Size = MaxCaptureBytes
	b.WAL = Member{Presence: Absent}
	if _, err := Select(l.Previous.Scope, &l, fixtureObservation(), &b, testNow.Add(time.Minute)); err != nil {
		t.Fatal("exact-bound source with absent SHM must remain admissible", err)
	}
	b.SHM = b.DB
	b.SHM.Inode = 99
	b.SHM.ProofSHA256 = ""
	b.SHM.Size = 32768
	if b.Validate() != nil {
		t.Fatal("SHM fixture is not a safe regular member")
	}
	_, err := Select(l.Previous.Scope, &l, fixtureObservation(), &b, testNow.Add(time.Minute))
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CoverageUnavailable {
		t.Fatalf("source exceeds the reused captureSQLiteSource budget by %d bytes but was admitted: %v", b.SHM.Size, err)
	}
}

func TestConsumerUsesCurrentAuthorizationWithoutRewritingPoint(t *testing.T) {
	h, p := fixturePrepared(Fresh, LTX)
	now := p.PreparedAt
	current := h.Intent.Scope
	current.CredentialVersion++
	current.PolicySHA256 = hash("3")
	current.Retention.EvidenceSHA256 = hash("4")
	current.Retention.ObservedAt = now
	current.Retention.FreshUntil = now.Add(time.Hour)
	current.Retention.RetainUntil = h.Intent.Scope.Retention.RetainUntil.Add(time.Hour)
	c := Consumer{PreparationID: p.PreparationID, OperationID: "consumer", Requester: h.Intent.Task.Requester, App: h.Intent.Scope.App, InputSHA256: hash("1"), Kind: ReviewedConsumer, ReviewedPlanSHA256: hash("2"), PreparedSHA256: mustHash(p), PolicySHA256: current.PolicySHA256, RetentionSHA256: current.Retention.EvidenceSHA256, Bounds: &data.InitializationBounds{MaxBackupAgeSeconds: 120, MaxRestoreTestAgeSeconds: 30, RecoveryWindowSeconds: 3600}, ClaimedAt: now, RecoverUntil: now.Add(time.Hour)}
	if !c.Admits(h.Intent, p, current, now) {
		t.Fatal("current protected authorization rejected for unchanged point")
	}
	if p.IntentSHA256 != mustHash(h.Intent) || c.PreparedSHA256 != mustHash(p) {
		t.Fatal("renewal rewrote historical point or intent")
	}
	current.BindingID = data.ReplicaBindingID(id("9"))
	if c.Admits(h.Intent, p, current, now) {
		t.Fatal("current authorization crossed source binding")
	}
}
