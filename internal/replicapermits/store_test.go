package replicapermits

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
)

type permitStoreFake struct {
	permit store.ReplicaPermit
	schema store.WriterSchema
	err    error
	reads  int
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
	return f.schema, f.err
}

type configFilesFake struct {
	raw []byte
	err error
}

func (f configFilesFake) ReadConfig(context.Context, string) ([]byte, error) { return f.raw, f.err }

func storePermitFixture(t testing.TB) (store.ReplicaPermit, replication.Binding, []byte) {
	t.Helper()
	b := replication.Binding{DatabaseID: strings.Repeat("1", 32), BindingID: strings.Repeat("2", 32), EpochID: strings.Repeat("3", 32), IncarnationID: strings.Repeat("4", 32), DBPath: "/srv/data/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/app.db", SocketPath: "/srv/state/replication/" + strings.Repeat("2", 32) + "/control.sock", Endpoint: "https://objects.example.invalid", Bucket: "backup-bucket", Prefix: "base/apps/" + strings.Repeat("4", 32) + "/databases/" + strings.Repeat("1", 32) + "/epochs/" + strings.Repeat("3", 32) + "/", Region: "auto", ForcePathStyle: true, Cadence: replication.DefaultCadence()}
	raw, err := replication.RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
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
	r := StorePermits{State: state, Configs: configFilesFake{raw: raw}}
	if err := replication.WriterPermit(context.Background(), r, b.IncarnationID); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("missing current schema allowed", err)
	}
	state.schema = store.WriterSchema{ReleaseID: "committed-release", Bindings: []data.DatabaseBinding{p.Database}}
	if err := replication.WriterPermit(context.Background(), r, b.IncarnationID); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("missing fresh on-disk schema allowed", err)
	}
	if state.reads != 4 {
		t.Fatal("schema or permits cached", state.reads)
	}
}
