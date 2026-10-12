package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

type maintenanceRig struct {
	*dispositionRig
	plans        map[string]MaintenancePlan
	deletion     Deletion
	proof        RetentionProof
	fail         string
	effects      int
	now          time.Time
	health       []CredentialHealth
	retentionOK  bool
	beforeDelete func(int, bool)
}

func (r *maintenanceRig) SaveMaintenancePlan(_ context.Context, p MaintenancePlan) error {
	r.plans[p.ID] = p
	return nil
}
func (r *maintenanceRig) LoadMaintenancePlan(_ context.Context, id string) (MaintenancePlan, error) {
	p, ok := r.plans[id]
	if !ok {
		return p, ErrRefused
	}
	return p, nil
}
func (r *maintenanceRig) ReadArchiveDeletion(context.Context, data.ArchiveID) (Deletion, error) {
	return r.deletion, nil
}
func (r *maintenanceRig) AdvanceArchiveDeletion(_ context.Context, old, next Deletion) error {
	if r.deletion != old || !DeletionNext(old, next, len(r.p.Trees)) {
		return ErrRefused
	}
	if r.fail == string(next.Stage) {
		return ErrRecovery
	}
	r.deletion = next
	return nil
}
func (r *maintenanceRig) SaveRetentionProof(_ context.Context, p RetentionProof) error {
	r.proof = p
	return nil
}
func (r *maintenanceRig) ReadRetentionProof(context.Context, data.ArchiveID) (RetentionProof, error) {
	return r.proof, nil
}
func (r *maintenanceRig) ArchiveMaintenanceAdmission(context.Context, Plan, Disposition) (MaintenanceAdmission, error) {
	if r.fail == "ownership" || r.fail == "running" {
		return MaintenanceAdmission{}, ErrRefused
	}
	return MaintenanceAdmission{RetentionAdmitted: r.retentionOK, PolicyHash: r.p.PolicyHash, TargetHash: r.p.TargetHash}, nil
}
func (r *maintenanceRig) VerifyArchiveRetention(context.Context, Plan, Manifest, Disposition) (RetentionProof, error) {
	if r.fail == "credentials" || r.fail == "remote" || r.fail == "retention" {
		return RetentionProof{}, ErrRefused
	}
	return RetentionProof{ArchiveID: r.p.ArchiveID, VerifiedAt: r.now, ValidUntil: r.now.Add(MaxEvidenceAge), RetainUntil: r.state.RetainUntil}, nil
}
func (r *maintenanceRig) DeleteArchivedTree(ctx context.Context, p Plan, reviewed MaintenancePlan, index int, adopt bool) error {
	r.effects++
	if r.beforeDelete != nil {
		r.beforeDelete(index, adopt)
	}
	if r.fail == "effect" {
		r.fail = ""
		if err := data.DeleteArchiveTree(ctx, p.Trees[index], p.Candidate.Incarnation, p.ArchiveID, reviewed.Parents[index], adopt); err != nil {
			return err
		}
		return ErrRecovery
	}
	return data.DeleteArchiveTree(ctx, p.Trees[index], p.Candidate.Incarnation, p.ArchiveID, reviewed.Parents[index], adopt)
}
func maintenanceFixture(t *testing.T) (MaintenanceService, *maintenanceRig) {
	t.Helper()
	s, r := dispositionFixture(t)
	if _, err := s.Retire(context.Background(), r.p.ArchiveID); err != nil {
		t.Fatal(err)
	}
	rig := &maintenanceRig{dispositionRig: r, plans: map[string]MaintenancePlan{}, now: time.Now().UTC()}
	return MaintenanceService{Journal: rig, Host: rig, Requester: s.Requester, Authorize: s.Authorize, Lock: s.Lock, Now: func() time.Time { return rig.now }}, rig
}
func TestArchiveRetentionClockAndExpiryBoundary(t *testing.T) {
	s, r := maintenanceFixture(t)
	ctx := context.Background()
	r.now = r.state.RetainUntil.Add(-time.Nanosecond)
	if _, err := s.Plan(ctx, r.p.ArchiveID, ExpireArchive); !errors.Is(err, ErrRefused) {
		t.Fatal("early expiry admitted", err)
	}
	p, err := s.Plan(ctx, r.p.ArchiveID, VerifyRetention)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Verify(ctx, p.ID)
	if err != nil || !proof.RetainUntil.Equal(r.state.RetainUntil) || proof.ValidUntil.After(r.state.RetainUntil) {
		t.Fatal(proof, err)
	}
	r.now = r.state.RetainUntil
	p, err = s.Plan(ctx, r.p.ArchiveID, ExpireArchive)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.Delete(ctx, p.ID); err != nil || out.Stage != DeletionComplete {
		t.Fatal(out, err)
	}
	if err = data.VerifyArchivedTree(r.p.Trees[0], r.p.Candidate.Incarnation, r.p.ArchiveID); err == nil {
		t.Fatal("expiry retained local bytes")
	}
}
func TestArchivePurgeNeedsExactReviewedPlanAndNeverChangesRetentionClock(t *testing.T) {
	s, r := maintenanceFixture(t)
	ctx := context.Background()
	deadline := r.state.RetainUntil
	if _, err := s.Delete(ctx, string(r.p.ArchiveID)); !errors.Is(err, ErrRefused) {
		t.Fatal("unreviewed purge admitted", err)
	}
	p, err := s.Plan(ctx, r.p.ArchiveID, PurgeArchive)
	if err != nil {
		t.Fatal(err)
	}
	r.fail = "ownership"
	if _, err = s.Delete(ctx, p.ID); !errors.Is(err, ErrRefused) || r.effects != 0 || r.deletion.Sequence != 0 {
		t.Fatal("live/reused ownership purged", err)
	}
	r.fail = ""
	if _, err = s.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if !r.state.RetainUntil.Equal(deadline) {
		t.Fatal("purge rewrote removal history")
	}
}
func TestArchivePurgeReconcilesDeletionEffectAndReceiptWithoutOtherEffects(t *testing.T) {
	for _, boundary := range []string{string(TreeDeletionIntent), "effect", string(TreeDeleted), string(DeletionComplete)} {
		t.Run(boundary, func(t *testing.T) {
			s, r := maintenanceFixture(t)
			ctx := context.Background()
			p, err := s.Plan(ctx, r.p.ArchiveID, PurgeArchive)
			if err != nil {
				t.Fatal(err)
			}
			r.fail = string(boundary)
			if _, err = s.Delete(ctx, p.ID); err == nil {
				t.Fatal("interruption not injected")
			}
			r.fail = ""
			r.now = r.now.Add(10 * time.Minute)
			if out, err := s.ReconcileDelete(ctx, r.p.ArchiveID); err != nil || out.Stage != DeletionComplete {
				t.Fatal(out, err)
			}
			before := r.effects
			if out, err := s.ReconcileDelete(ctx, r.p.ArchiveID); err != nil || out.Stage != DeletionComplete || r.effects != before {
				t.Fatal("terminal deletion repeated", out, err)
			}
		})
	}
}
func TestArchiveRetentionFailureNeverRelabelsOldProofFresh(t *testing.T) {
	for _, failure := range []string{"credentials", "remote", "retention"} {
		t.Run(failure, func(t *testing.T) {
			s, r := maintenanceFixture(t)
			ctx := context.Background()
			p, err := s.Plan(ctx, r.p.ArchiveID, VerifyRetention)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := s.Verify(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			r.now = r.now.Add(time.Minute)
			r.fail = failure
			p, err = s.Plan(ctx, r.p.ArchiveID, VerifyRetention)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Verify(ctx, p.ID); err == nil || !r.proof.VerifiedAt.Equal(proof.VerifiedAt) {
				t.Fatal("failed retention called fresh", err)
			}
		})
	}
}

func TestArchiveDeletionCannotInferAuthorityBeforeDurableIntent(t *testing.T) {
	s, r := maintenanceFixture(t)
	ctx := context.Background()
	p, err := s.Plan(ctx, r.p.ArchiveID, PurgeArchive)
	if err != nil {
		t.Fatal(err)
	}
	r.fail = string(DeletionIntent)
	if _, err = s.Delete(ctx, p.ID); err == nil || r.effects != 0 || r.deletion.Sequence != 0 {
		t.Fatal("failed claim caused effects", err)
	}
	r.fail = ""
	if _, err = s.ReconcileDelete(ctx, r.p.ArchiveID); !errors.Is(err, ErrRecovery) || r.effects != 0 {
		t.Fatal("saved plan inferred execution authority", err)
	}
}

func (r *maintenanceRig) ArchiveCredentialHealth(context.Context, Plan) ([]CredentialHealth, error) {
	return r.health, nil
}

func TestArchiveMaintenanceStatusPreservesProofAgeAndReportsTerminalStates(t *testing.T) {
	s, r := maintenanceFixture(t)
	ctx := context.Background()
	if status, err := s.Status(ctx, r.p.ArchiveID); err != nil || status.State != "unverified" {
		t.Fatal(status, err)
	}
	p, err := s.Plan(ctx, r.p.ArchiveID, VerifyRetention)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Verify(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := s.Status(ctx, r.p.ArchiveID); err != nil || status.State != "verified" || status.Proof == nil || !status.Proof.VerifiedAt.Equal(proof.VerifiedAt) {
		t.Fatal(status, err)
	}
	r.now = r.now.Add(MaxEvidenceAge + time.Second)
	if status, err := s.Status(ctx, r.p.ArchiveID); err != nil || status.State != "degraded" || status.Proof == nil || !status.Proof.VerifiedAt.Equal(proof.VerifiedAt) {
		t.Fatal("stale proof relabeled", status, err)
	}
	r.now = r.state.RetainUntil
	if status, err := s.Status(ctx, r.p.ArchiveID); err != nil || status.State != "expired" {
		t.Fatal(status, err)
	}
	p, err = s.Plan(ctx, r.p.ArchiveID, ExpireArchive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if status, err := s.Status(ctx, r.p.ArchiveID); err != nil || status.State != "local_deleted" || status.Proof == nil || !status.Proof.VerifiedAt.Equal(proof.VerifiedAt) {
		t.Fatal(status, err)
	}
}

func TestArchiveMaintenanceApplyCannotSelectAnotherArchive(t *testing.T) {
	s, r := maintenanceFixture(t)
	p, err := s.Plan(context.Background(), r.p.ArchiveID, PurgeArchive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), data.ArchiveID("11111111111111111111111111111111"), p.ID); !errors.Is(err, ErrRefused) || r.effects != 0 || r.deletion.Sequence != 0 {
		t.Fatal("cross-archive reviewed plan applied", err)
	}
}

func multiRootMaintenanceFixture(t *testing.T) (MaintenanceService, *maintenanceRig) {
	t.Helper()
	service, prepared := preparationFixture(t)
	p := prepared.p
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { // #nosec G302 -- Private fixture directory requires owner traversal.
		t.Fatal(err)
	}
	second := p.Candidate.Databases[0]
	second.sqlite.Binding.DatabaseID = data.DatabaseID(strings.Repeat("f", 32))
	second.sqlite.Binding.ReplicaBindingID = data.ReplicaBindingID(strings.Repeat("1", 32))
	second.sqlite.Binding.Name = "second"
	second.sqlite.Binding.Root = data.PersistentRoot(root)
	relative, err := data.RelativeDirectory(second.sqlite.Binding.IncarnationID, second.sqlite.Binding.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	second.sqlite.Binding.RelativeDirectory = relative
	second.sqlite.Binding.MountPath = "/second"
	if err = os.MkdirAll(filepath.Join(root, relative), 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := data.CaptureAllocation(second.sqlite.Binding)
	if err != nil {
		t.Fatal(err)
	}
	second.sqlite.Allocation = &allocation
	epoch := second.sqlite.Epochs[0]
	epoch.DatabaseID = second.sqlite.Binding.DatabaseID
	epoch.BindingID = second.sqlite.Binding.ReplicaBindingID
	epoch.EpochID = data.ReplicaEpochID(strings.Repeat("2", 32))
	epoch.RemotePrefix, err = data.RemotePrefix(epoch.Destination.BasePrefix, p.Candidate.Incarnation, epoch.DatabaseID, epoch.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	second.sqlite.Epochs = []data.ReplicaBinding{epoch}
	p.Candidate.Databases = append(p.Candidate.Databases, second)
	tree, err := data.CaptureArchiveTree(second.sqlite.Binding.Root, p.Candidate.Incarnation, p.ArchiveID)
	if err != nil {
		t.Fatal(err)
	}
	p.Trees = append(p.Trees, tree)
	slices.SortFunc(p.Trees, func(a, b data.ArchiveTree) int { return strings.Compare(string(a.Root), string(b.Root)) })
	p.ID, err = p.Fingerprint()
	if err != nil || !p.Valid() {
		t.Fatal("multi-root plan", err)
	}
	prepared.p = p
	if _, err = service.Prepare(context.Background(), p.Candidate.App, p.ID); err != nil {
		t.Fatal(err)
	}
	disposition := &dispositionRig{preparationRig: prepared}
	service.Journal = disposition
	service.Host = disposition
	if _, err = service.Retire(context.Background(), p.ArchiveID); err != nil {
		t.Fatal(err)
	}
	rig := &maintenanceRig{dispositionRig: disposition, plans: map[string]MaintenancePlan{}, now: time.Now().UTC()}
	return MaintenanceService{Journal: rig, Host: rig, Requester: p.Requester, Authorize: service.Authorize, Lock: service.Lock, Now: func() time.Time { return rig.now }}, rig
}

func TestArchiveMaintenanceMultiRootReconcilesOnlyOrderedDurableEffects(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(fmt.Sprint(tamper), func(t *testing.T) {
			s, r := multiRootMaintenanceFixture(t)
			ctx := context.Background()
			plan, err := s.Plan(ctx, r.p.ArchiveID, PurgeArchive)
			if err != nil {
				t.Fatal(err)
			}
			r.fail = "effect"
			if _, err = s.Apply(ctx, r.p.ArchiveID, plan.ID); !errors.Is(err, ErrRecovery) || r.deletion.TreeIndex != 0 || r.deletion.Stage != TreeDeletionIntent {
				t.Fatal("lost first-root unknown outcome", r.deletion, err)
			}
			second := r.p.Trees[1]
			if data.VerifyArchivedTree(second, r.p.Candidate.Incarnation, r.p.ArchiveID) != nil {
				t.Fatal("later root changed before its durable intent")
			}
			if tamper {
				if err = data.DeleteArchiveTree(ctx, second, r.p.Candidate.Incarnation, r.p.ArchiveID, plan.Parents[1], false); err != nil {
					t.Fatal(err)
				}
				if _, err = s.ReconcileDelete(ctx, r.p.ArchiveID); !errors.Is(err, ErrRecovery) || r.deletion.TreeIndex != 0 || r.effects != 1 {
					t.Fatal("out-of-order absence adopted", r.deletion, err)
				}
				return
			}
			out, err := s.ReconcileDelete(ctx, r.p.ArchiveID)
			if err != nil || out.Stage != DeletionComplete || out.TreeIndex != 1 {
				t.Fatal("ordered multi-root recovery", out, err)
			}
			for i, tree := range r.p.Trees {
				present, err := data.InspectArchiveDeletionParent(tree, r.p.Candidate.Incarnation, r.p.ArchiveID, plan.Parents[i])
				if err != nil || present {
					t.Fatal("root not deleted", i, err)
				}
			}
		})
	}
}

func TestArchiveInitializedRetentionProofRequiresFreshExactRemoteEvidence(t *testing.T) {
	p, database, coverage, preparedAt := initializedCoverage(t)
	manifest := Manifest{Plan: p, OperationID: strings.Repeat("a", 32), Coverage: []Coverage{coverage}, QuiescedAt: preparedAt, PreparedAt: preparedAt}
	retiredAt := preparedAt.Add(time.Second)
	disposition := Disposition{ArchiveID: p.ArchiveID, Stage: Retired, CommittedAt: retiredAt, RetainUntil: retiredAt.Add(30 * 24 * time.Hour)}
	now := retiredAt.Add(time.Hour)
	fresh := *coverage.sqlite.Receipt
	fresh.OperationID = "archive-retention-test"
	fresh.ObservedAt = now
	fresh.LossWindow.To = now
	proof := RetentionProof{ArchiveID: p.ArchiveID, PolicyHash: p.PolicyHash, TargetHash: p.TargetHash, VerifiedAt: now, ValidUntil: now.Add(MaxEvidenceAge), RetainUntil: disposition.RetainUntil, Receipts: []restore.SQLiteReceipt{fresh}}
	if !RetentionProofMatches(proof, p, disposition, manifest, now) {
		t.Fatal("fresh initialized retained proof refused")
	}
	for _, kind := range []string{"old", "source", "cutoff", "schema", "missing", "deadline", "invariants"} {
		t.Run(kind, func(t *testing.T) {
			changed := proof
			changed.Receipts = append([]restore.SQLiteReceipt(nil), proof.Receipts...)
			plan := p
			originalManifest := manifest
			switch kind {
			case "old":
				changed.Receipts[0] = *coverage.sqlite.Receipt
			case "source":
				source := *fresh.Source.Snapshot
				source.SHA256 = strings.Repeat("b", 64)
				changed.Receipts[0].Source.Snapshot = &source
			case "cutoff":
				c := *fresh.Coverage
				c.CoveredThrough = c.CoveredThrough.Add(time.Second)
				changed.Receipts[0].Coverage = &c
			case "schema":
				changed.Receipts[0].Schema.Marker = "changed"
			case "missing":
				changed.Receipts = nil
			case "deadline":
				changed.ValidUntil = disposition.RetainUntil.Add(time.Second)
			case "invariants":
				database.sqlite.Invariants = []data.RestoreInvariant{{Database: database.sqlite.Binding.Name, Kind: "row_count", Table: "commits", Count: 1}}
				plan.Candidate.Databases = []Database{database}
				plan.ID, _ = plan.Fingerprint()
				originalManifest.Plan = plan
			}
			if RetentionProofMatches(changed, plan, disposition, originalManifest, now) {
				t.Fatal("unbound initialized retention accepted", kind)
			}
		})
	}
}

func TestArchiveMaintenanceHealthCannotHideMissingOrDifferentEpochs(t *testing.T) {
	p, _, _, now := initializedCoverage(t)
	binding := p.Candidate.Databases[0].sqlite.Epochs[0]
	health := CredentialHealth{BindingID: binding.BindingID, EpochID: binding.EpochID, ReceivedAt: now.Add(-time.Minute), AgeSeconds: 60}
	if !credentialHealthMatches(p, []CredentialHealth{health}, now) {
		t.Fatal("exact health refused")
	}
	if credentialHealthMatches(p, nil, now) || credentialHealthMatches(p, []CredentialHealth{health, health}, now) {
		t.Fatal("missing or duplicated initialized epoch admitted")
	}
	health.EpochID = data.ReplicaEpochID(strings.Repeat("1", 32))
	if credentialHealthMatches(p, []CredentialHealth{health}, now) {
		t.Fatal("recreated epoch health used")
	}
	health.EpochID = binding.EpochID
	expiry := now.Add(-time.Second)
	health.ExpiresAt = &expiry
	if credentialHealthMatches(p, []CredentialHealth{health}, now) {
		t.Fatal("expired credential reported healthy")
	}
	health.Expired = true
	health.RenewalNeeded = true
	if !credentialHealthMatches(p, []CredentialHealth{health}, now) {
		t.Fatal("affirmative expired metadata refused")
	}
}

func TestArchiveInitializedStatusReportsUnknownLifecycleWithoutRefreshingEvidence(t *testing.T) {
	s, r := maintenanceFixture(t)
	_, _, coverage, preparedAt := initializedCoverage(t)
	r.p.Candidate.Databases[0].sqlite.Written = true
	var err error
	r.p.ID, err = r.p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	r.m = Manifest{Plan: r.p, OperationID: strings.Repeat("a", 32), Coverage: []Coverage{coverage}, QuiescedAt: preparedAt, PreparedAt: preparedAt}
	r.now = preparedAt.Add(time.Second)
	binding := r.p.Candidate.Databases[0].sqlite.Epochs[0]
	r.health = []CredentialHealth{{BindingID: binding.BindingID, EpochID: binding.EpochID, ReceivedAt: preparedAt, AgeSeconds: 1}}
	status, err := s.Status(context.Background(), r.p.ArchiveID)
	if err != nil || status.State != "degraded" || status.Reason != "destination_retention_unknown" || status.Proof != nil {
		t.Fatal("unknown lifecycle reported healthy or refreshed", status, err)
	}
	r.retentionOK = true
	status, err = s.Status(context.Background(), r.p.ArchiveID)
	if err != nil || status.State != "unverified" || status.Proof != nil {
		t.Fatal("status invented remote verification", status, err)
	}
}

func TestArchiveMaintenanceReconcileCannotAdoptNewTreeAbsence(t *testing.T) {
	s, r := multiRootMaintenanceFixture(t)
	ctx := context.Background()
	plan, err := s.Plan(ctx, r.p.ArchiveID, PurgeArchive)
	if err != nil {
		t.Fatal(err)
	}
	r.fail = "effect"
	if _, err = s.Apply(ctx, r.p.ArchiveID, plan.ID); !errors.Is(err, ErrRecovery) || r.deletion.Stage != TreeDeletionIntent || r.deletion.TreeIndex != 0 {
		t.Fatal("expected earlier in-flight first-tree deletion", r.deletion, err)
	}
	second := r.p.Trees[1]
	bundle := filepath.Join(string(second.Root), second.ArchiveRelative)
	marker := filepath.Join(bundle, "preserved")
	if err = os.WriteFile(marker, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(string(second.Root), "retained-away")
	firstAdopt, laterAdopt := false, false
	r.beforeDelete = func(index int, adopt bool) {
		if index == 0 {
			firstAdopt = adopt
			return
		}
		laterAdopt = adopt
		// Move after fresh location validation, just before the host deletion call.
		// This run has written the second intent, but has never deleted this tree.
		if err := os.Rename(bundle, moved); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.ReconcileDelete(ctx, r.p.ArchiveID)
	if !errors.Is(err, ErrRecovery) || out.Stage != TreeDeletionIntent || out.TreeIndex != 1 || r.deletion != out || !firstAdopt || laterAdopt {
		t.Fatal("new tree absence adopted as a prior deletion effect", out, err, firstAdopt, laterAdopt)
	}
	raw, err := os.ReadFile(filepath.Join(moved, "preserved")) // #nosec G304 -- Private fixture moved within its own temporary root.
	if err != nil || string(raw) != "untouched" {
		t.Fatal("later tree was changed", err)
	}
}
