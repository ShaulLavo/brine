package replicapermits

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
)

type permitStoreFake struct {
	permit         store.ReplicaPermit
	schema         store.WriterSchema
	err            error
	reads          int
	start          store.WriterStartResolution
	candidateReads int
	committedReads int
}

func (f *permitStoreFake) ReadReplicaPermitByBinding(context.Context, data.ReplicaBindingID) (store.ReplicaPermit, error) {
	f.reads++
	return f.permit, f.err
}
func (f *permitStoreFake) ReadWriterPermits(context.Context, data.AppIncarnationID) ([]store.ReplicaPermit, error) {
	f.reads++
	return []store.ReplicaPermit{f.permit}, f.err
}
func (f *permitStoreFake) ReadWriterSchema(context.Context, data.AppIncarnationID) (store.WriterSchema, error) {
	f.reads++
	f.committedReads++
	return f.schema, f.err
}

type configFilesFake struct {
	raw []byte
	err error
}

func (f configFilesFake) ReadConfig(context.Context, string) ([]byte, error) { return f.raw, f.err }

func storePermitFixture(t testing.TB) (store.ReplicaPermit, replication.Binding, []byte) {
	t.Helper()
	b := replication.Binding{DatabaseID: strings.Repeat("1", 32), BindingID: strings.Repeat("2", 32), EpochID: strings.Repeat("3", 32), IncarnationID: strings.Repeat("4", 32), DBPath: "/srv/data/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/app.db", SocketPath: "/srv/state/replication/" + strings.Repeat("2", 32) + "/control.sock", Endpoint: "https://objects.example.invalid", Bucket: "backup-bucket", Prefix: "base/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/epochs/" + strings.Repeat("3", 32) + "/", Region: "auto", ForcePathStyle: true, Cadence: replication.Cadence{SyncInterval: time.Minute, SnapshotInterval: 6 * time.Hour}}
	raw, err := replication.RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G101 -- CredentialPath is a synthetic fixture path, not a credential.
	p := store.ReplicaPermit{DBPath: b.DBPath, SocketPath: b.SocketPath, ConfigPath: path.Join(path.Dir(b.SocketPath), "litestream.yml"), CredentialPath: "/srv/state/credentials/s3/primary/v1.env", LifetimeLockPath: "/srv/state/replica-locks/" + b.BindingID + ".lock", FenceState: "unfenced", DestinationOwnership: "local", SourceSettled: true, Fences: []data.QuiescenceFence{}}
	p.Database = data.DatabaseBinding{DatabaseID: data.DatabaseID(b.DatabaseID), IncarnationID: data.AppIncarnationID(b.IncarnationID), ReplicaBindingID: data.ReplicaBindingID(b.BindingID)}
	p.Replica = data.ReplicaBinding{BindingID: data.ReplicaBindingID(b.BindingID), DatabaseID: data.DatabaseID(b.DatabaseID), EpochID: data.ReplicaEpochID(b.EpochID), Destination: data.Destination{Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Bucket, PathStyle: b.ForcePathStyle}, RemotePrefix: b.Prefix, ConfigContent: string(raw), ConfigSHA256: strings.TrimPrefix(replication.ConfigHash(raw), "sha256:"), ConfigFile: p.ConfigPath, SocketFile: p.SocketPath, CredentialFile: p.CredentialPath, LifetimeLockFile: p.LifetimeLockPath, Committed: true}
	return p, b, raw
}

func TestStorePermitsReadFreshCommittedConfigAndFence(t *testing.T) {
	p, b, raw := storePermitFixture(t)
	state := &permitStoreFake{permit: p}
	r := StorePermits{State: state, Configs: configFilesFake{raw: raw}}
	ctx := context.Background()
	request := replication.ReplicaPermitRequest{DatabaseID: b.DatabaseID, BindingID: b.BindingID, EpochID: b.EpochID, ConfigHash: replication.ConfigHash(raw)}
	if err := replication.ReplicaPermit(ctx, r, request); err != nil {
		t.Fatal(err)
	}
	if state.reads != 1 {
		t.Fatal("not a fresh store read")
	}
	state.permit.FenceState = "held"
	if err := replication.ReplicaPermit(ctx, r, request); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("held fence admitted", err)
	}
	s, err := r.ReadReplicaPermit(ctx, b.BindingID)
	if err != nil || s.Fence != replication.FenceHeld {
		t.Fatal("quiescence cannot inspect held fence", err)
	}
	state.permit.FenceState = "unfenced"
	r.Configs = configFilesFake{raw: []byte(strings.Replace(string(raw), "sync-interval: 1m0s", "sync-interval: 10s", 1))}
	if err := replication.ReplicaPermit(ctx, r, request); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("actual disk config drift admitted", err)
	}
	r.Configs = configFilesFake{err: errors.New("unreadable")}
	if err := replication.ReplicaPermit(ctx, r, request); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("unreadable config admitted", err)
	}
}

func TestStorePermitsRefuseUnsettledOrInconsistentProjection(t *testing.T) {
	for name, mutate := range map[string]func(*store.ReplicaPermit){
		"uncommitted":     func(p *store.ReplicaPermit) { p.Replica.Committed = false },
		"stored-hash":     func(p *store.ReplicaPermit) { p.Replica.ConfigSHA256 = strings.Repeat("f", 64) },
		"database":        func(p *store.ReplicaPermit) { p.Replica.DatabaseID = data.DatabaseID(strings.Repeat("f", 32)) },
		"missing-fences":  func(p *store.ReplicaPermit) { p.Fences = nil },
		"unknown-fence":   func(p *store.ReplicaPermit) { p.FenceState = "unknown" },
		"credential-path": func(p *store.ReplicaPermit) { p.CredentialPath = "/different" },
		"socket-path":     func(p *store.ReplicaPermit) { p.SocketPath = "/different" },
		"lock-path":       func(p *store.ReplicaPermit) { p.LifetimeLockPath = "/different" },
	} {
		t.Run(name, func(t *testing.T) {
			p, b, raw := storePermitFixture(t)
			mutate(&p)
			r := StorePermits{State: &permitStoreFake{permit: p}, Configs: configFilesFake{raw: raw}}
			if _, err := r.ReadReplicaPermit(context.Background(), b.BindingID); err == nil {
				t.Fatal("inconsistent projection allowed")
			}
		})
	}
}

func TestStoreWriterPermitsNeverCacheSchemaCompatibility(t *testing.T) {
	p, b, raw := storePermitFixture(t)
	state := &permitStoreFake{permit: p}
	r := StorePermits{State: state, Configs: configFilesFake{raw: raw}, Writers: &writerEvidenceFake{match: true}}
	if err := replication.WriterPermit(context.Background(), r, b.IncarnationID); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("missing current schema allowed", err)
	}
	state.schema = store.WriterSchema{ReleaseID: "committed-release", Bindings: []data.DatabaseBinding{p.Database}}
	if err := replication.WriterPermit(context.Background(), r, b.IncarnationID); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("missing fresh on-disk schema allowed", err)
	}
	if state.reads != 6 {
		t.Fatal("schema or permits cached", state.reads)
	}
}

func (f *permitStoreFake) ReadWriterStart(context.Context, data.AppIncarnationID) (store.WriterStartResolution, error) {
	f.reads++
	if f.start.State == "" {
		return store.WriterStartResolution{State: store.WriterStartNone}, f.err
	}
	return f.start, f.err
}
func (f *permitStoreFake) CandidateWriterSchema(context.Context, data.AppIncarnationID, policy.Desired) (store.WriterSchema, error) {
	f.candidateReads++
	return f.schema, f.err
}

type writerEvidenceFake struct {
	active, match          bool
	err                    error
	activeReads, unitReads int
}

func (f *writerEvidenceFake) OperationActive(context.Context, string) (bool, error) {
	f.activeReads++
	return f.active, f.err
}
func (f *writerEvidenceFake) CommittedUnitsMatch(context.Context, []target.Unit) (bool, error) {
	f.unitReads++
	return f.match, f.err
}
func allocatedWriterFixture(t *testing.T) (StorePermits, *permitStoreFake, *writerEvidenceFake, string) {
	t.Helper()
	p, b, _ := storePermitFixture(t)
	root := t.TempDir()
	relative, err := data.RelativeDirectory(p.Database.IncarnationID, p.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- Owner-only traversal is required for the private fixture directory.
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, relative), 0700); err != nil {
		t.Fatal(err)
	}
	p.Database.Name = "main"
	p.Database.Root = data.PersistentRoot(root)
	p.Database.RelativeDirectory = relative
	p.Database.Filename = "app.db"
	p.Database.MountPath = "/data"
	p.DBPath = filepath.Join(root, relative, "app.db")
	b.DBPath = p.DBPath
	raw, err := replication.RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	p.Replica.ConfigContent = string(raw)
	p.Replica.ConfigSHA256 = strings.TrimPrefix(replication.ConfigHash(raw), "sha256:")
	receipt, err := data.CaptureAllocation(p.Database)
	if err != nil {
		t.Fatal(err)
	}
	state := &permitStoreFake{permit: p, schema: store.WriterSchema{ReleaseID: "release", Bindings: []data.DatabaseBinding{p.Database}, Desired: policy.Desired{SchemaCompatibility: []data.SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{data.EmptyMarker}}}}, Allocations: map[data.DatabaseID]data.AllocationReceipt{p.Database.DatabaseID: receipt}, Units: []target.Unit{{Name: "app.container", Hash: "sha256:" + strings.Repeat("a", 64)}}}}
	evidence := &writerEvidenceFake{active: true, match: true}
	return StorePermits{State: state, Configs: configFilesFake{raw: raw}, Writers: evidence}, state, evidence, b.IncarnationID
}
func TestPendingWriterRequiresLiveOperationAndNeverFallsBack(t *testing.T) {
	r, state, evidence, id := allocatedWriterFixture(t)
	state.start = store.WriterStartResolution{State: store.WriterStartPending, Intent: &store.WriterStartIntent{OperationID: "operation", IncarnationID: data.AppIncarnationID(id), Desired: state.schema.Desired}}
	if err := replication.WriterPermit(context.Background(), r, id); err != nil {
		t.Fatal(err)
	}
	if state.candidateReads != 1 || state.committedReads != 0 {
		t.Fatal("wrong schema authority")
	}
	evidence.active = false
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("reboot intent admitted")
	}
	state.start.State = store.WriterStartInvalid
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("invalid intent fell back")
	}
	if state.committedReads != 0 {
		t.Fatal("candidate fell back to old release")
	}
}
func TestCommittedWriterRequiresOwnedUnitsAndFreshAllocation(t *testing.T) {
	r, state, evidence, id := allocatedWriterFixture(t)
	if err := replication.WriterPermit(context.Background(), r, id); err != nil {
		t.Fatal(err)
	}
	evidence.match = false
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("foreign installed unit admitted")
	}
	evidence.match = true
	state.schema.Allocations = nil
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("missing receipt inferred untouched after crash")
	}
	evidence.err = errors.New("unknown")
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("unknown unit admitted")
	}
}

func TestWriterUsesRegistryDefinitionsAndObservesSchemaAfresh(t *testing.T) {
	r, state, _, id := allocatedWriterFixture(t)
	db, err := sql.Open("sqlite", state.permit.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err = db.Exec("CREATE TABLE t(x TEXT);" + data.MarkerTableSQL); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(state.permit.DBPath, 0600); err != nil {
		t.Fatal(err)
	}
	statement := "CREATE TABLE t(x TEXT)"
	_, hash, err := data.CatalogFingerprint([]data.CatalogRow{{Type: "table", Name: "t", TableName: "t", SQL: &statement}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO brine_schema_marker VALUES(1,?,?)", "v1", hash); err != nil {
		t.Fatal(err)
	}
	state.schema.Allocations = nil
	state.schema.Desired.SchemaCompatibility[0].Accepts = []string{"v1"}
	state.schema.Definitions = []data.SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: hash}}
	// Candidate declarations are not the authoritative registry definition.
	state.schema.Desired.SchemaDefinitions = []data.SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: strings.Repeat("f", 64)}}
	if err := replication.WriterPermit(context.Background(), r, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE changed(x TEXT)"); err != nil {
		t.Fatal(err)
	}
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("schema verdict cached across catalog change")
	}
}

func TestWriterBridgeRejectsIncompleteOrExtraCompatibilitySets(t *testing.T) {
	r, state, _, id := allocatedWriterFixture(t)
	state.schema.Desired.SchemaCompatibility = append(state.schema.Desired.SchemaCompatibility, data.SchemaCompatibility{Database: "extra", Startup: "preserve", Accepts: []string{data.EmptyMarker}})
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("extra compatibility declaration admitted")
	}
	state.schema.Desired.SchemaCompatibility = nil
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("missing compatibility declaration admitted")
	}
}
