package datainit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

type memoryJournal struct {
	point   VerifiedRestorePoint
	plan    Plan
	op      Operation
	claimed bool
	fail    string
	events  []string
}

func (j *memoryJournal) SaveInitPlan(_ context.Context, p Plan) error       { j.plan = p; return nil }
func (j *memoryJournal) LoadInitPlan(context.Context, string) (Plan, error) { return j.plan, nil }
func (j *memoryJournal) ReadInitOperation(context.Context, string) (Operation, bool, error) {
	return j.op, j.claimed, nil
}
func (j *memoryJournal) ClaimInitialization(_ context.Context, p Plan) (Operation, error) {
	j.claimed = true
	j.op = Operation{ID: strings.Repeat("1", 32), PlanID: p.ID, Fence: data.FenceID(strings.Repeat("2", 32)), State: "intent"}
	j.events = append(j.events, "intent")
	if j.fail == "intent" {
		return j.op, ErrRecovery
	}
	return j.op, nil
}
func (j *memoryJournal) SetInitState(_ context.Context, o Operation, state string) (Operation, error) {
	if j.fail == state {
		return o, ErrRecovery
	}
	j.op = o
	j.op.State = state
	j.events = append(j.events, state)
	return j.op, nil
}

func initFixture(t *testing.T) (Service, *memoryJournal, Request) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	} // #nosec G302 -- Private directory requires owner traversal.

	inc := data.AppIncarnationID(strings.Repeat("1", 32))
	id := data.DatabaseID(strings.Repeat("2", 32))
	relative, err := data.RelativeDirectory(inc, id)
	if err != nil {
		t.Fatal(err)
	}
	b := data.DatabaseBinding{DatabaseID: id, IncarnationID: inc, Name: "main", Root: data.PersistentRoot(root), RelativeDirectory: relative, MountPath: "/data", Filename: "app.db", ReplicaBindingID: data.ReplicaBindingID(strings.Repeat("3", 32))}
	if err = os.MkdirAll(filepath.Join(root, relative), 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := data.CaptureAllocation(b)
	if err != nil {
		t.Fatal(err)
	}
	definition := data.SchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}
	artifact := data.SchemaInitializer{Definition: definition, Statements: []string{"CREATE TABLE t(x TEXT)"}}
	hash := "sha256:" + strings.Repeat("a", 64)
	r := Request{App: "example", FirstReleasePlan: hash, Artifact: hash}
	j := &memoryJournal{}
	requester, err := AgentRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	s := Service{PrepareRestorePoint: func(_ context.Context, p Plan, o Operation) (VerifiedRestorePoint, error) {
		return fixturePoint(p, o), nil
	}, Journal: j, Requester: requester, Authorize: func(context.Context) error { return nil }, Lock: func(context.Context) (func(), error) { return func() {}, nil }, Quiesce: func(context.Context, Plan) (func(), error) { return func() {}, nil }, Facts: func(ctx context.Context, _ Request) (Facts, error) {
		return Facts{Plan: Plan{ReplicaEpoch: data.ReplicaEpochID(strings.Repeat("4", 32)), RemotePrefix: "prefix/epoch", Bounds: data.InitializationBounds{MaxBackupAgeSeconds: 300, MaxRestoreTestAgeSeconds: 300, RecoveryWindowSeconds: 3600}, PolicyHash: hash, TargetHash: hash, DesiredHash: hash, Database: b, Definition: definition}, Initializer: artifact, Allocation: &allocation, Observation: data.ObserveSchemaWithAllocation(ctx, b, []data.SchemaDefinition{definition}, &allocation)}, nil
	}}
	return s, j, r
}

func TestInitializationBoundaryInterruptionNeverReplays(t *testing.T) {
	for _, boundary := range []string{"intent", "quiesce", "quiesced", "restore_point_intent", "prepare_restore_point", "restore_receipt", "restore_point_verified", "mutation_intent", "mutation_completed", "succeeded"} {
		t.Run(boundary, func(t *testing.T) {
			s, j, r := initFixture(t)
			ctx := context.Background()
			p, err := s.Plan(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			j.fail = boundary
			if boundary == "prepare_restore_point" {
				s.PrepareRestorePoint = func(context.Context, Plan, Operation) (VerifiedRestorePoint, error) {
					return VerifiedRestorePoint{}, ErrRecovery
				}
			}
			if boundary == "quiesce" {
				s.Quiesce = func(context.Context, Plan) (func(), error) { return nil, ErrRecovery }
			}
			if _, err = s.Apply(ctx, r.App, p.ID); err == nil {
				t.Fatal("interruption accepted as complete")
			}
			if !j.claimed {
				t.Fatal("intent lost")
			}
			j.fail = ""
			s.Quiesce = func(context.Context, Plan) (func(), error) { t.Fatal("unknown attempt replayed"); return nil, nil }
			op, err := s.Apply(ctx, r.App, p.ID)
			committed := boundary == "mutation_completed" || boundary == "succeeded"
			if committed {
				if err != nil || op.State != "succeeded" {
					t.Fatalf("committed schema not adopted: %+v %v", op, err)
				}
			} else if !errors.Is(err, ErrRecovery) {
				t.Fatalf("empty/unknown attempt not retained for recovery: %v", err)
			}
		})
	}
}
func TestInitializationAuthorityAndEvidenceRefusals(t *testing.T) {
	for _, reason := range []string{"agent-policy", "non-empty", "unknown", "missing-restore", "incompatible-first-release", "stale-policy"} {
		t.Run(reason, func(t *testing.T) {
			s, j, r := initFixture(t)
			facts := s.Facts
			switch reason {
			case "agent-policy":
				s.Authorize = func(context.Context) error { return ErrRefused }
			case "missing-restore", "incompatible-first-release":
				s.Facts = func(context.Context, Request) (Facts, error) { return Facts{}, ErrRefused }
			case "non-empty", "unknown":
				s.Facts = func(ctx context.Context, r Request) (Facts, error) {
					f, e := facts(ctx, r)
					f.Observation.State = data.Unknown
					return f, e
				}
			}
			p, err := s.Plan(context.Background(), r)
			if reason != "stale-policy" {
				if err == nil || j.claimed {
					t.Fatal("unsafe planning accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s.Facts = func(ctx context.Context, r Request) (Facts, error) {
				f, e := facts(ctx, r)
				f.Plan.PolicyHash = "sha256:" + strings.Repeat("b", 64)
				return f, e
			}
			if _, err = s.Apply(context.Background(), r.App, p.ID); err == nil || j.claimed {
				t.Fatal("stale plan applied")
			}
		})
	}
}
func TestInitializationSuccessAndRepeatedInspection(t *testing.T) {
	s, j, r := initFixture(t)
	p, err := s.Plan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.Apply(context.Background(), r.App, p.ID)
	if err != nil || op.State != "succeeded" {
		t.Fatalf("apply: %+v %v", op, err)
	}
	count := len(j.events)
	s.Quiesce = func(context.Context, Plan) (func(), error) { t.Fatal("completed operation replayed"); return nil, nil }
	if _, err = s.Apply(context.Background(), r.App, p.ID); err != nil {
		t.Fatal(err)
	}
	if len(j.events) > count+1 {
		t.Fatal("replayed effects")
	}
}

func fixturePoint(p Plan, o Operation) VerifiedRestorePoint {
	now := time.Now().UTC()
	snapshot := restore.SnapshotSource{BindingID: string(p.Database.ReplicaBindingID), Epoch: string(p.ReplicaEpoch), PointID: p.RestorePointID, ObjectKey: p.RemotePrefix + "/restore-points/" + p.RestorePointID + "/snapshot.sqlite", SHA256: strings.Repeat("a", 64), Size: 4096}
	return VerifiedRestorePoint{PointID: p.RestorePointID, DatabaseID: p.Database.DatabaseID, UploadedAt: now, RetainUntil: now.Add(2 * time.Hour), Receipt: restore.Receipt{ToolVersion: restore.SnapshotToolVersion, OperationID: o.ID + "-empty-verify", Source: restore.RestoreSource{Kind: restore.SQLiteSnapshot, Snapshot: &snapshot}, ObservedAt: now, Schema: restore.SchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed"}}
}
func (j *memoryJournal) SaveInitRestorePoint(_ context.Context, _ Operation, p VerifiedRestorePoint) error {
	if j.fail == "restore_receipt" {
		return ErrRecovery
	}
	j.point = p
	return nil
}
func (j *memoryJournal) LoadInitRestorePoint(context.Context, Operation) (VerifiedRestorePoint, error) {
	if j.point.PointID == "" {
		return VerifiedRestorePoint{}, ErrRecovery
	}
	return j.point, nil
}

func TestInitializationRestoreEvidenceRefusalsBeforeMutation(t *testing.T) {
	for _, reason := range []string{"missing", "stale-upload", "stale-test", "wrong-epoch", "wrong-key", "wrong-binding", "retention", "non-empty", "failed-integrity", "wrong-operation"} {
		t.Run(reason, func(t *testing.T) {
			s, j, r := initFixture(t)
			s.PrepareRestorePoint = func(_ context.Context, p Plan, o Operation) (VerifiedRestorePoint, error) {
				point := fixturePoint(p, o)
				switch reason {
				case "missing":
					return VerifiedRestorePoint{}, nil
				case "stale-upload":
					point.UploadedAt = time.Now().Add(-time.Hour)
				case "stale-test":
					point.UploadedAt = time.Now().Add(-time.Hour)
					point.Receipt.ObservedAt = point.UploadedAt
				case "wrong-epoch":
					point.Receipt.Source.Snapshot.Epoch = strings.Repeat("5", 32)
				case "wrong-key":
					point.Receipt.Source.Snapshot.ObjectKey = "foreign/snapshot.sqlite"
				case "wrong-binding":
					point.Receipt.Source.Snapshot.BindingID = strings.Repeat("5", 32)
				case "retention":
					point.RetainUntil = time.Now()
				case "non-empty":
					point.Receipt.Schema.State = restore.VerifiedSchema
				case "failed-integrity":
					point.Receipt.IntegrityCheck = "failed"
				case "wrong-operation":
					point.Receipt.OperationID = "foreign-proof"
				}
				return point, nil
			}
			p, err := s.Plan(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(context.Background(), r.App, p.ID); !errors.Is(err, ErrRecovery) {
				t.Fatalf("unsafe evidence accepted: %v", err)
			}
			f, err := s.Facts(context.Background(), r)
			if err != nil || f.Observation.State != data.AllocatedEmpty {
				t.Fatal("schema mutated before verified restore point")
			}
			if !j.claimed {
				t.Fatal("recovery intent lost")
			}
		})
	}
}

func TestInitializationAgentCannotApplyLocalOperatorPlan(t *testing.T) {
	local, j, request := initFixture(t)
	agent := local.Requester
	local.Requester = LocalOperatorRequester()
	p, err := local.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	remote := local
	remote.Requester = agent
	if _, err = remote.Apply(context.Background(), request.App, p.ID); !errors.Is(err, ErrRefused) || j.claimed {
		t.Fatal("remote requester reused local operator plan", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var retained Plan
	if err = json.Unmarshal(raw, &retained); err != nil || !retained.Valid() || !retained.Requester.IsLocalOperator() {
		t.Fatal("local authority was not retained by immutable plan", err)
	}
	reconstructed := local
	reconstructed.Requester = retained.Requester
	op, err := reconstructed.Apply(context.Background(), request.App, retained.ID)
	if err != nil || op.State != "succeeded" {
		t.Fatal("local initialization lost reconstructed authority", err)
	}
}
