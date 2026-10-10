// Package datainit owns reviewed, first-release-only empty SQLite initialization.
package datainit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

var ErrRefused = errors.New("empty-data initialization refused")
var ErrRecovery = errors.New("empty-data initialization requires inspection")
var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var digest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidID(id string) bool { return digest.MatchString(id) }

type Request struct {
	App              string `json:"app"`
	FirstReleasePlan string `json:"first_release_plan"`
	Artifact         string `json:"artifact"`
}

type Plan struct {
	ReplicaEpoch   data.ReplicaEpochID       `json:"replica_epoch"`
	RemotePrefix   string                    `json:"remote_prefix"`
	RestorePointID string                    `json:"restore_point_id"`
	Bounds         data.InitializationBounds `json:"bounds"`
	ID             string                    `json:"id"`
	Request        Request                   `json:"request"`
	Requester      string                    `json:"requester"`
	PolicyHash     string                    `json:"policy_hash"`
	TargetHash     string                    `json:"target_hash"`
	Generation     uint64                    `json:"generation"`
	DesiredHash    string                    `json:"desired_hash"`
	Database       data.DatabaseBinding      `json:"database"`
	Definition     data.SchemaDefinition     `json:"definition"`
	Source         data.SchemaState          `json:"source"`
}

func (p Plan) fingerprint() (string, error) {
	p.ID = ""
	raw, err := json.Marshal(struct {
		Plan      Plan   `json:"plan"`
		Requester string `json:"requester"`
	}{p, p.Requester})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func (p Plan) Valid() bool {
	id, err := p.fingerprint()
	return err == nil && id == p.ID && ValidID(p.ID) && p.Requester != "" && appName.MatchString(p.Request.App) && ValidID(p.Request.FirstReleasePlan) && ValidID(p.Request.Artifact) && data.ValidID(p.RestorePointID) && data.ValidID(string(p.ReplicaEpoch)) && data.ValidID(string(p.Database.ReplicaBindingID)) && p.RemotePrefix != "" && p.Bounds.Valid() && ValidID(p.PolicyHash) && ValidID(p.TargetHash) && ValidID(p.DesiredHash) && data.ValidID(string(p.Database.DatabaseID)) && data.ValidID(string(p.Database.IncarnationID)) && p.Definition.Database == p.Database.Name && p.Definition.Marker != data.EmptyMarker && data.ValidMarker(p.Definition.Marker) && data.ValidCatalogHash(p.Definition.CatalogSHA256) && (p.Source == data.AllocatedEmpty || p.Source == data.VerifiedEmpty)
}

type Facts struct {
	Plan        Plan
	Initializer data.SchemaInitializer
	Allocation  *data.AllocationReceipt
	Observation data.SchemaObservation
}

type VerifiedRestorePoint struct {
	PointID     string          `json:"point_id"`
	DatabaseID  data.DatabaseID `json:"database_id"`
	UploadedAt  time.Time       `json:"uploaded_at"`
	RetainUntil time.Time       `json:"retain_until"`
	Receipt     restore.Receipt `json:"receipt"`
}

func (point VerifiedRestorePoint) Admits(p Plan, now time.Time) bool {
	snapshot := point.Receipt.Source.Snapshot
	return snapshot != nil && point.PointID == p.RestorePointID && point.DatabaseID == p.Database.DatabaseID && snapshot.PointID == point.PointID && snapshot.BindingID == string(p.Database.ReplicaBindingID) && snapshot.Epoch == string(p.ReplicaEpoch) && snapshot.ObjectKey == p.RemotePrefix+"/restore-points/"+p.RestorePointID+"/snapshot.sqlite" && snapshot.Size > 0 && snapshot.Size <= 1<<20 && data.ValidCatalogHash(snapshot.SHA256) && point.Receipt.Source.Kind == restore.SQLiteSnapshot && point.Receipt.Schema.State == restore.VerifiedEmpty && point.Receipt.Schema.Marker == data.EmptyMarker && point.Receipt.Schema.CatalogSHA256 == data.EmptyCatalogSHA256 && point.Receipt.IntegrityCheck == "passed" && point.Receipt.ForeignKeyCheck == "passed" && point.Receipt.InvariantCheck == "passed" && !point.UploadedAt.IsZero() && !point.Receipt.ObservedAt.Before(point.UploadedAt) && !now.Before(point.Receipt.ObservedAt) && now.Sub(point.UploadedAt) <= time.Duration(p.Bounds.MaxBackupAgeSeconds)*time.Second && now.Sub(point.Receipt.ObservedAt) <= time.Duration(p.Bounds.MaxRestoreTestAgeSeconds)*time.Second && !point.RetainUntil.Before(now.Add(time.Duration(p.Bounds.RecoveryWindowSeconds)*time.Second))
}

type Operation struct {
	ID     string       `json:"id"`
	PlanID string       `json:"plan_id"`
	State  string       `json:"state"`
	Fence  data.FenceID `json:"fence_id"`
}

// Journal atomically records intent and a durable database fence. A plan may
// have only one attempt, including after an unknown result. SetState appends
// a reference-only journal entry. Completion releases the fence atomically.
type Journal interface {
	SaveInitPlan(context.Context, Plan) error
	LoadInitPlan(context.Context, string) (Plan, error)
	ReadInitOperation(context.Context, string) (Operation, bool, error)
	ClaimInitialization(context.Context, Plan) (Operation, error)
	SetInitState(context.Context, Operation, string) (Operation, error)
	SaveInitRestorePoint(context.Context, Operation, VerifiedRestorePoint) error
	LoadInitRestorePoint(context.Context, Operation) (VerifiedRestorePoint, error)
}

type Service struct {
	Journal             Journal
	Requester           string
	Authorize           func(context.Context) error
	Lock                func(context.Context) (func(), error)
	Facts               func(context.Context, Request) (Facts, error)
	Quiesce             func(context.Context, Plan) (func(), error)
	PrepareRestorePoint func(context.Context, Plan, Operation) (VerifiedRestorePoint, error)
}

func (s Service) guard(ctx context.Context) (func(), error) {
	if s.Journal == nil || s.Authorize == nil || s.Lock == nil || s.Facts == nil || s.Quiesce == nil || s.PrepareRestorePoint == nil || s.Requester == "" {
		return nil, ErrRefused
	}
	if err := s.Authorize(ctx); err != nil {
		return nil, ErrRefused
	}
	return s.Lock(ctx)
}
func (s Service) Plan(ctx context.Context, r Request) (Plan, error) {
	release, err := s.guard(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer release()
	f, err := s.Facts(ctx, r)
	if err != nil {
		return Plan{}, ErrRefused
	}
	p := f.Plan
	p.Request = r
	p.Requester = s.Requester
	p.Source = f.Observation.State
	if p.Source != data.AllocatedEmpty && p.Source != data.VerifiedEmpty {
		return Plan{}, ErrRefused
	}
	if err = f.Initializer.Validate(ctx); err != nil {
		return Plan{}, ErrRefused
	}
	p.RestorePointID, err = data.NewID()
	if err != nil {
		return Plan{}, err
	}
	p.ID, err = p.fingerprint()
	if err != nil || !p.Valid() {
		return Plan{}, ErrRefused
	}
	return p, s.Journal.SaveInitPlan(ctx, p)
}
func (s Service) Apply(ctx context.Context, app, id string) (Operation, error) {
	release, err := s.guard(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer release()
	p, err := s.Journal.LoadInitPlan(ctx, id)
	if err != nil || !p.Valid() || p.Requester != s.Requester || p.Request.App != app {
		return Operation{}, ErrRefused
	}
	old, exists, err := s.Journal.ReadInitOperation(ctx, id)
	if err != nil {
		return Operation{}, err
	}
	f, err := s.Facts(ctx, p.Request)
	if err != nil {
		if exists {
			return old, ErrRecovery
		}
		return Operation{}, ErrRefused
	}
	fresh := f.Plan
	fresh.ID = p.ID
	fresh.Request = p.Request
	fresh.Requester = p.Requester
	fresh.Source = p.Source
	fresh.RestorePointID = p.RestorePointID
	if !fresh.Valid() {
		if exists {
			return old, ErrRecovery
		}
		return Operation{}, ErrRefused
	}
	if exists {
		point, pointErr := s.Journal.LoadInitRestorePoint(ctx, old)
		if pointErr != nil || !point.Admits(p, point.Receipt.ObservedAt) || point.Receipt.OperationID != old.ID+"-empty-verify" {
			return old, ErrRecovery
		}
		if f.Observation.State != data.VerifiedSchema || f.Observation.Marker != p.Definition.Marker || f.Observation.CatalogSHA256 != p.Definition.CatalogSHA256 {
			return old, ErrRecovery
		}
		// Reconciliation adopts only the freshly verified destination. It never
		// invokes the initializer, even when the source is still affirmatively empty.
		return s.Journal.SetInitState(ctx, old, "succeeded")
	}
	if f.Observation.State != p.Source {
		return Operation{}, ErrRefused
	}
	op, err := s.Journal.ClaimInitialization(ctx, p)
	if err != nil {
		return Operation{}, err
	}
	stopped, err := s.Quiesce(ctx, p)
	if err != nil {
		return op, ErrRecovery
	}
	defer stopped()
	op, err = s.Journal.SetInitState(ctx, op, "quiesced")
	if err != nil {
		return op, ErrRecovery
	}
	op, err = s.Journal.SetInitState(ctx, op, "restore_point_intent")
	if err != nil {
		return op, ErrRecovery
	}
	point, err := s.PrepareRestorePoint(ctx, p, op)
	if err != nil || !point.Admits(p, time.Now().UTC()) || point.Receipt.OperationID != op.ID+"-empty-verify" {
		return op, ErrRecovery
	}
	if err = s.Journal.SaveInitRestorePoint(ctx, op, point); err != nil {
		return op, ErrRecovery
	}
	op, err = s.Journal.SetInitState(ctx, op, "restore_point_verified")
	if err != nil {
		return op, ErrRecovery
	}
	// Recheck evidence and actual emptiness AFTER quiescence, before intent.
	f, err = s.Facts(ctx, p.Request)
	if err != nil || (f.Observation.State != data.AllocatedEmpty && f.Observation.State != data.VerifiedEmpty) {
		return op, ErrRecovery
	}
	fresh = f.Plan
	fresh.ID = p.ID
	fresh.Request = p.Request
	fresh.Requester = p.Requester
	fresh.Source = p.Source
	fresh.RestorePointID = p.RestorePointID
	if !fresh.Valid() || !point.Admits(p, time.Now().UTC()) {
		return op, ErrRecovery
	}
	op, err = s.Journal.SetInitState(ctx, op, "mutation_intent")
	if err != nil {
		return op, ErrRecovery
	}
	if err = data.InitializeSchema(ctx, p.Database, f.Initializer, f.Allocation); err != nil {
		return op, ErrRecovery
	}
	op, err = s.Journal.SetInitState(ctx, op, "mutation_completed")
	if err != nil {
		return op, ErrRecovery
	}
	observation := data.ObserveSchema(ctx, p.Database, []data.SchemaDefinition{p.Definition})
	if observation.State != data.VerifiedSchema || observation.Marker != p.Definition.Marker || observation.CatalogSHA256 != p.Definition.CatalogSHA256 {
		return op, ErrRecovery
	}
	return s.Journal.SetInitState(ctx, op, "succeeded")
}
