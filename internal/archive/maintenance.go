package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

type MaintenanceKind string

const (
	VerifyRetention MaintenanceKind = "verify_retention"
	PurgeArchive    MaintenanceKind = "purge_archive"
	ExpireArchive   MaintenanceKind = "expire_archive"
)

type MaintenanceAdmission struct {
	RetentionAdmitted bool   `json:"retention_admitted"`
	PurgeAllowed      bool   `json:"purge_allowed,omitempty"`
	PolicyHash        string `json:"policy_hash"`
	TargetHash        string `json:"target_hash"`
}
type MaintenancePlan struct {
	ID            string                       `json:"id"`
	ArchiveID     data.ArchiveID               `json:"archive_id"`
	ArchivePlanID string                       `json:"archive_plan_id"`
	Kind          MaintenanceKind              `json:"kind"`
	Requester     string                       `json:"requester"`
	PolicyHash    string                       `json:"policy_hash"`
	TargetHash    string                       `json:"target_hash"`
	CreatedAt     time.Time                    `json:"created_at"`
	RetiredAt     time.Time                    `json:"retired_at"`
	RetainUntil   time.Time                    `json:"retain_until"`
	Parents       []data.ArchiveDeletionParent `json:"parents"`
}

func (p MaintenancePlan) Fingerprint() (string, error) {
	p.ID = ""
	raw, err := json.Marshal(p)
	if err != nil {
		return "", ErrRefused
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func (p MaintenancePlan) Valid() bool {
	id, err := p.Fingerprint()
	if len(p.Parents) == 0 || len(p.Parents) > 16 {
		return false
	}
	for i, parent := range p.Parents {
		if !data.ValidRoot(string(parent.Root)) || parent.Device == 0 || parent.Inode == 0 || i > 0 && p.Parents[i-1].Root >= parent.Root {
			return false
		}
	}
	return err == nil && id == p.ID && data.ValidID(string(p.ArchiveID)) && digest.MatchString(p.ArchivePlanID) && digest.MatchString(p.PolicyHash) && digest.MatchString(p.TargetHash) && requesterName.MatchString(p.Requester) && !p.CreatedAt.IsZero() && !p.RetiredAt.IsZero() && !p.CreatedAt.Before(p.RetiredAt) && p.RetainUntil.Equal(p.RetiredAt.Add(30*24*time.Hour)) && (p.Kind == VerifyRetention || p.Kind == PurgeArchive || p.Kind == ExpireArchive)
}

type RetentionProof struct {
	PolicyHash  string                  `json:"policy_hash"`
	TargetHash  string                  `json:"target_hash"`
	ArchiveID   data.ArchiveID          `json:"archive_id"`
	VerifiedAt  time.Time               `json:"verified_at"`
	ValidUntil  time.Time               `json:"valid_until"`
	RetainUntil time.Time               `json:"retain_until"`
	Receipts    []restore.SQLiteReceipt `json:"receipts,omitempty"`
}
type DeletionStage string

const (
	DeletionIntent     DeletionStage = "delete_intent"
	TreeDeletionIntent DeletionStage = "tree_delete_intent"
	TreeDeleted        DeletionStage = "tree_deleted"
	DeletionComplete   DeletionStage = "deleted"
)

type Deletion struct {
	ArchiveID   data.ArchiveID  `json:"archive_id"`
	PlanID      string          `json:"plan_id"`
	Kind        MaintenanceKind `json:"kind"`
	Sequence    uint64          `json:"sequence"`
	Stage       DeletionStage   `json:"stage"`
	TreeIndex   int             `json:"tree_index"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt time.Time       `json:"completed_at,omitempty"`
}

func DeletionNext(old, next Deletion, trees int) bool {
	if !data.ValidID(string(next.ArchiveID)) || !digest.MatchString(next.PlanID) || next.Kind != PurgeArchive && next.Kind != ExpireArchive || trees < 1 || trees > 16 || next.TreeIndex < 0 || next.TreeIndex >= trees || next.Sequence != old.Sequence+1 || next.StartedAt.IsZero() || next.Stage != DeletionComplete && !next.CompletedAt.IsZero() {
		return false
	}
	if old.Sequence != 0 && (old.ArchiveID != next.ArchiveID || old.PlanID != next.PlanID || old.Kind != next.Kind || !old.StartedAt.Equal(next.StartedAt)) {
		return false
	}
	switch old.Stage {
	case "":
		return old.Sequence == 0 && next.Stage == DeletionIntent && next.TreeIndex == 0
	case DeletionIntent:
		return next.Stage == TreeDeletionIntent && next.TreeIndex == 0
	case TreeDeletionIntent:
		return next.Stage == TreeDeleted && next.TreeIndex == old.TreeIndex
	case TreeDeleted:
		if old.TreeIndex+1 < trees {
			return next.Stage == TreeDeletionIntent && next.TreeIndex == old.TreeIndex+1
		}
		return next.Stage == DeletionComplete && next.TreeIndex == old.TreeIndex && !next.CompletedAt.Before(next.StartedAt) && !next.CompletedAt.IsZero()
	default:
		return false
	}
}

type MaintenanceJournal interface {
	LoadArchivePlanByArchive(context.Context, data.ArchiveID) (Plan, error)
	ReadArchiveDisposition(context.Context, data.ArchiveID) (Disposition, bool, error)
	ReadArchiveManifest(context.Context, data.ArchiveID) (Manifest, error)
	SaveMaintenancePlan(context.Context, MaintenancePlan) error
	LoadMaintenancePlan(context.Context, string) (MaintenancePlan, error)
	ReadArchiveDeletion(context.Context, data.ArchiveID) (Deletion, error)
	AdvanceArchiveDeletion(context.Context, Deletion, Deletion) error
	SaveRetentionProof(context.Context, RetentionProof) error
	ReadRetentionProof(context.Context, data.ArchiveID) (RetentionProof, error)
}
type MaintenanceHost interface {
	ArchiveMaintenanceAdmission(context.Context, Plan, Disposition) (MaintenanceAdmission, error)
	ArchiveCredentialHealth(context.Context, Plan) ([]CredentialHealth, error)
	AcquireLifetimeLocks(context.Context, Plan) (func() error, error)
	VerifyArchiveRetention(context.Context, Plan, Manifest, Disposition) (RetentionProof, error)
	DeleteArchivedTree(context.Context, Plan, MaintenancePlan, int, bool) error
}
type MaintenanceService struct {
	Journal   MaintenanceJournal
	Host      MaintenanceHost
	Requester string
	Authorize func(context.Context) error
	Lock      func(context.Context) (func() error, error)
	Now       func() time.Time
}

func (s MaintenanceService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s MaintenanceService) guard(ctx context.Context) (func() error, error) {
	if s.Journal == nil || s.Host == nil || s.Authorize == nil || s.Lock == nil || !requesterName.MatchString(s.Requester) || s.Authorize(ctx) != nil {
		return nil, ErrRefused
	}
	return s.Lock(ctx)
}
func (s MaintenanceService) retained(ctx context.Context, id data.ArchiveID) (p Plan, d Disposition, m Manifest, err error) {
	if !data.ValidID(string(id)) {
		return p, d, m, ErrRefused
	}
	p, err = s.Journal.LoadArchivePlanByArchive(ctx, id)
	if err != nil || !p.Valid() || p.ArchiveID != id || p.Requester != s.Requester {
		return p, d, m, ErrRefused
	}
	var found bool
	d, found, err = s.Journal.ReadArchiveDisposition(ctx, id)
	if err != nil || !found || d.Stage != Retired || d.ArchiveID != id || !d.RetainUntil.Equal(d.CommittedAt.Add(30*24*time.Hour)) {
		return p, d, m, ErrRefused
	}
	m, err = s.Journal.ReadArchiveManifest(ctx, id)
	if err != nil || !m.Valid() || m.Plan.ID != p.ID {
		return p, d, m, ErrRefused
	}
	return p, d, m, nil
}
func verifyDeletionLocations(p Plan, reviewed MaintenancePlan, d Deletion, adopt bool) error {
	for i, t := range p.Trees {
		var present bool
		var err error
		if len(reviewed.Parents) == 0 && !adopt && d.Sequence == 0 {
			present, err = data.InspectArchiveDeletion(t, p.Candidate.Incarnation, p.ArchiveID)
		} else if len(reviewed.Parents) == len(p.Trees) {
			present, err = data.InspectArchiveDeletionParent(t, p.Candidate.Incarnation, p.ArchiveID, reviewed.Parents[i])
		} else {
			return ErrRecovery
		}
		if err != nil {
			return ErrRecovery
		}
		deleted := d.Stage == DeletionComplete || d.Stage == TreeDeleted && i <= d.TreeIndex || d.Stage == TreeDeletionIntent && i < d.TreeIndex
		uncertain := adopt && d.Stage == TreeDeletionIntent && i == d.TreeIndex
		if deleted && present || !deleted && !present && !uncertain {
			return ErrRecovery
		}
	}
	return nil
}
func verifyArchiveAllocations(p Plan) error {
	for _, d := range p.Candidate.Databases {
		if native, ok := d.Native(); ok {
			if native.Allocation == nil || data.VerifyNativeArchivedAllocation(native.Binding, *native.Allocation, p.ArchiveID) != nil {
				return ErrRefused
			}
			continue
		}
		sqlDatabase, sqlite := d.SQLite()
		if !sqlite || sqlDatabase.Allocation == nil || data.VerifyArchivedAllocation(sqlDatabase.Binding, *sqlDatabase.Allocation, p.ArchiveID) != nil {
			return ErrRefused
		}
	}
	return nil
}
func (s MaintenanceService) Plan(ctx context.Context, id data.ArchiveID, kind MaintenanceKind) (out MaintenancePlan, err error) {
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	p, d, _, err := s.retained(ctx, id)
	if err != nil {
		return out, err
	}
	if kind != VerifyRetention && kind != PurgeArchive && kind != ExpireArchive || kind == ExpireArchive && s.now().Before(d.RetainUntil) || kind == VerifyRetention && !s.now().Before(d.RetainUntil) {
		return out, ErrRefused
	}
	cursor, err := s.Journal.ReadArchiveDeletion(ctx, id)
	if err != nil || cursor.Sequence != 0 || verifyDeletionLocations(p, MaintenancePlan{}, Deletion{}, false) != nil || verifyArchiveAllocations(p) != nil {
		return out, ErrRefused
	}
	admission, err := s.Host.ArchiveMaintenanceAdmission(ctx, p, d)
	if err != nil || admission.TargetHash != p.TargetHash || kind == PurgeArchive && !admission.PurgeAllowed {
		return out, ErrRefused
	}
	out = MaintenancePlan{ArchiveID: id, ArchivePlanID: p.ID, Kind: kind, Requester: s.Requester, PolicyHash: admission.PolicyHash, TargetHash: admission.TargetHash, CreatedAt: s.now(), RetiredAt: d.CommittedAt, RetainUntil: d.RetainUntil}
	for _, tree := range p.Trees {
		parent, captureErr := data.CaptureArchiveDeletionParent(tree, p.Candidate.Incarnation, p.ArchiveID)
		if captureErr != nil {
			return MaintenancePlan{}, ErrRefused
		}
		out.Parents = append(out.Parents, parent)
	}
	out.ID, err = out.Fingerprint()
	if err != nil || !out.Valid() {
		return MaintenancePlan{}, ErrRefused
	}
	return out, s.Journal.SaveMaintenancePlan(ctx, out)
}
func (s MaintenanceService) reviewed(ctx context.Context, id string) (plan MaintenancePlan, p Plan, d Disposition, m Manifest, err error) {
	plan, err = s.Journal.LoadMaintenancePlan(ctx, id)
	if err != nil || !plan.Valid() || plan.Requester != s.Requester {
		return plan, p, d, m, ErrRefused
	}
	p, d, m, err = s.retained(ctx, plan.ArchiveID)
	if err != nil || plan.ArchivePlanID != p.ID || !plan.RetiredAt.Equal(d.CommittedAt) || !plan.RetainUntil.Equal(d.RetainUntil) {
		return plan, p, d, m, ErrRefused
	}
	if len(plan.Parents) != len(p.Trees) {
		return plan, p, d, m, ErrRefused
	}
	for i, parent := range plan.Parents {
		if !parent.Valid(p.Trees[i]) {
			return plan, p, d, m, ErrRefused
		}
	}
	return plan, p, d, m, nil
}
func (s MaintenanceService) freshAdmission(ctx context.Context, plan MaintenancePlan, p Plan, d Disposition) error {
	now := s.now()
	admission, err := s.Host.ArchiveMaintenanceAdmission(ctx, p, d)
	if err != nil || plan.Kind == PurgeArchive && !admission.PurgeAllowed || plan.PolicyHash != admission.PolicyHash || plan.TargetHash != admission.TargetHash || now.Before(plan.CreatedAt) || now.Sub(plan.CreatedAt) > MaxEvidenceAge {
		return ErrRefused
	}
	return nil
}
func (s MaintenanceService) Verify(ctx context.Context, id string) (out RetentionProof, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	plan, p, d, m, err := s.reviewed(ctx, id)
	if err != nil || plan.Kind != VerifyRetention || !s.now().Before(d.RetainUntil) || s.freshAdmission(ctx, plan, p, d) != nil {
		return out, ErrRefused
	}
	cursor, err := s.Journal.ReadArchiveDeletion(ctx, p.ArchiveID)
	if err != nil || cursor.Sequence != 0 || verifyDeletionLocations(p, plan, cursor, false) != nil || verifyArchiveAllocations(p) != nil {
		return out, ErrRefused
	}
	unlock, err := s.Host.AcquireLifetimeLocks(ctx, p)
	if err != nil {
		return out, ErrRefused
	}
	defer func() { err = errors.Join(err, unlock()) }()
	out, err = s.Host.VerifyArchiveRetention(ctx, p, m, d)
	if err != nil {
		return RetentionProof{}, ErrRefused
	}
	out.PolicyHash = plan.PolicyHash
	out.TargetHash = plan.TargetHash
	if out.ValidUntil.After(d.RetainUntil) {
		out.ValidUntil = d.RetainUntil
	}
	if !RetentionProofMatches(out, p, d, m, s.now()) || s.freshAdmission(ctx, plan, p, d) != nil || verifyDeletionLocations(p, plan, cursor, false) != nil {
		return RetentionProof{}, ErrRefused
	}
	return out, s.Journal.SaveRetentionProof(ctx, out)
}
func RetentionProofMatches(proof RetentionProof, p Plan, d Disposition, m Manifest, now time.Time) bool {
	if !digest.MatchString(proof.PolicyHash) || proof.TargetHash != p.TargetHash || !p.Valid() || !m.Valid() || m.Plan.ID != p.ID || d.Stage != Retired || d.ArchiveID != p.ArchiveID || proof.VerifiedAt.IsZero() || proof.ArchiveID != p.ArchiveID || !proof.RetainUntil.Equal(d.RetainUntil) || now.Before(proof.VerifiedAt) || now.Sub(proof.VerifiedAt) > MaxEvidenceAge || !now.Before(proof.ValidUntil) || proof.ValidUntil.After(proof.VerifiedAt.Add(MaxEvidenceAge)) || proof.ValidUntil.After(d.RetainUntil) {
		return false
	}
	index := 0
	for i, database := range p.Candidate.Databases {
		if !database.Written() {
			continue
		}
		original, sqliteCoverage := m.Coverage[i].SQLite()
		sqlDatabase, sqliteDatabase := database.SQLite()
		if !sqliteCoverage || !sqliteDatabase || index >= len(proof.Receipts) || original.Receipt == nil {
			return false
		}
		receipt := proof.Receipts[index]
		index++
		source, sourceErr := json.Marshal(receipt.Source)
		wanted, wantedErr := json.Marshal(original.Receipt.Source)
		if sourceErr != nil || wantedErr != nil || string(source) != string(wanted) || !receipt.Valid() || now.Before(receipt.ObservedAt) || now.Sub(receipt.ObservedAt) > MaxEvidenceAge || receipt.Schema.State != restore.SchemaState(original.Observation.State) || receipt.Schema.Marker != original.Observation.Marker || receipt.Schema.CatalogSHA256 != original.Observation.CatalogSHA256 || receipt.Coverage == nil || !receipt.Coverage.CoveredThrough.Equal(original.WritersQuiescedAt) || receipt.LossWindow.State != restore.LossBounded || len(sqlDatabase.Invariants) > 0 && receipt.InvariantCheck != "passed" {
			return false
		}
	}
	return index == len(proof.Receipts)
}
func (s MaintenanceService) Delete(ctx context.Context, id string) (out Deletion, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	plan, p, d, _, err := s.reviewed(ctx, id)
	if err != nil || plan.Kind != PurgeArchive && plan.Kind != ExpireArchive {
		return out, ErrRefused
	}
	out, err = s.Journal.ReadArchiveDeletion(ctx, p.ArchiveID)
	if err != nil {
		return out, err
	}
	if out.Sequence != 0 {
		if out.Stage == DeletionComplete && out.PlanID == id {
			return out, nil
		}
		return out, ErrRecovery
	}
	if s.freshAdmission(ctx, plan, p, d) != nil || plan.Kind == ExpireArchive && s.now().Before(d.RetainUntil) || verifyDeletionLocations(p, plan, out, false) != nil || verifyArchiveAllocations(p) != nil {
		return out, ErrRefused
	}
	unlock, lockErr := s.Host.AcquireLifetimeLocks(ctx, p)
	if lockErr != nil {
		return out, ErrRefused
	}
	defer func() { err = errors.Join(err, unlock()) }()
	next := Deletion{ArchiveID: p.ArchiveID, PlanID: plan.ID, Kind: plan.Kind, Sequence: 1, Stage: DeletionIntent, StartedAt: s.now()}
	if err = s.Journal.AdvanceArchiveDeletion(ctx, out, next); err != nil {
		return out, ErrRecovery
	}
	return s.deleteRunLocked(ctx, p, d, plan, next, false)
}
func (s MaintenanceService) ReconcileDelete(ctx context.Context, id data.ArchiveID) (out Deletion, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	p, d, _, err := s.retained(ctx, id)
	if err != nil {
		return out, err
	}
	out, err = s.Journal.ReadArchiveDeletion(ctx, id)
	if err != nil || out.Sequence == 0 {
		return out, ErrRecovery
	}
	if out.Stage == DeletionComplete {
		return out, nil
	}
	plan, err := s.Journal.LoadMaintenancePlan(ctx, out.PlanID)
	if err != nil || !plan.Valid() || plan.Requester != s.Requester || plan.ArchiveID != id || plan.ArchivePlanID != p.ID || plan.Kind != out.Kind {
		return out, ErrRecovery
	}
	return s.deleteRun(ctx, p, d, plan, out, true)
}
func (s MaintenanceService) deleteRun(ctx context.Context, p Plan, d Disposition, plan MaintenancePlan, out Deletion, adopt bool) (result Deletion, err error) {
	result = out
	unlock, err := s.Host.AcquireLifetimeLocks(ctx, p)
	if err != nil {
		return out, ErrRecovery
	}
	defer func() { err = errors.Join(err, unlock()) }()
	return s.deleteRunLocked(ctx, p, d, plan, out, adopt)
}
func (s MaintenanceService) deleteRunLocked(ctx context.Context, p Plan, d Disposition, plan MaintenancePlan, out Deletion, adopt bool) (Deletion, error) {
	// Only an intent already in flight when reconciliation started can explain
	// an unknown prior deletion effect. New intents in this run cannot.
	adoptIndex := -1
	if adopt && out.Stage == TreeDeletionIntent {
		adoptIndex = out.TreeIndex
	}
	for out.Stage != DeletionComplete {
		adoptCurrent := out.Stage == TreeDeletionIntent && out.TreeIndex == adoptIndex
		if ctx.Err() != nil || out.Kind == ExpireArchive && s.now().Before(d.RetainUntil) || verifyDeletionLocations(p, plan, out, adoptCurrent) != nil {
			return out, ErrRecovery
		}
		admission, admissionErr := s.Host.ArchiveMaintenanceAdmission(ctx, p, d)
		if admissionErr != nil || admission.TargetHash != p.TargetHash {
			return out, ErrRecovery
		}
		next := out
		next.Sequence++
		switch out.Stage {
		case DeletionIntent:
			next.Stage = TreeDeletionIntent
		case TreeDeletionIntent:
			if err := s.Host.DeleteArchivedTree(ctx, p, plan, out.TreeIndex, adoptCurrent); err != nil {
				return out, ErrRecovery
			}
			next.Stage = TreeDeleted
		case TreeDeleted:
			if out.TreeIndex+1 < len(p.Trees) {
				next.TreeIndex++
				next.Stage = TreeDeletionIntent
			} else {
				next.Stage = DeletionComplete
				next.CompletedAt = s.now()
			}
		default:
			return out, ErrRecovery
		}
		if err := s.Journal.AdvanceArchiveDeletion(ctx, out, next); err != nil {
			return out, ErrRecovery
		}
		out = next
	}
	return out, nil
}

type MaintenanceOutcome struct {
	Kind     MaintenanceKind `json:"kind"`
	Proof    *RetentionProof `json:"verification,omitempty"`
	Deletion *Deletion       `json:"deletion,omitempty"`
}

func (s MaintenanceService) Apply(ctx context.Context, archiveID data.ArchiveID, id string) (out MaintenanceOutcome, err error) {
	if s.Journal == nil || !data.ValidID(string(archiveID)) || !digest.MatchString(id) {
		return out, ErrRefused
	}
	plan, err := s.Journal.LoadMaintenancePlan(ctx, id)
	if err != nil || !plan.Valid() || plan.ArchiveID != archiveID || plan.Requester != s.Requester {
		return out, ErrRefused
	}
	out.Kind = plan.Kind
	if plan.Kind == VerifyRetention {
		proof, err := s.Verify(ctx, id)
		if err != nil {
			return MaintenanceOutcome{}, err
		}
		out.Proof = &proof
	} else {
		deletion, err := s.Delete(ctx, id)
		if err != nil {
			return MaintenanceOutcome{}, err
		}
		out.Deletion = &deletion
	}
	return out, nil
}
