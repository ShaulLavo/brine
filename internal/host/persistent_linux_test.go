//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type preparedServices struct {
	starts int
	active bool
}

func (s *preparedServices) Start(context.Context, string) error {
	s.starts++
	s.active = true
	return nil
}
func (s *preparedServices) Stop(context.Context, string) error { s.active = false; return nil }
func (s *preparedServices) ReplicaStopped(context.Context, string) (bool, error) {
	return !s.active, nil
}

func TestDataPreparationPublishesCommitsActivatesAndProvesExactArtifacts(t *testing.T) {
	ctx := context.Background()
	_, desired, facts := persistentHostFixture(t)
	stateRoot, err := os.MkdirTemp(os.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateRoot) })
	state, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	facts.Store = state
	fact := collectPersistent(t, facts, desired)
	home := t.TempDir()
	if err = os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err = ensurePrivateChild(stateRoot, "credentials/s3/primary"); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(stateRoot, "credentials/s3/primary/v1.env")
	if err = os.WriteFile(credentialPath, []byte("AWS_ACCESS_KEY_ID=fixture-access\nAWS_SECRET_ACCESS_KEY=fixture-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	permit, err := state.ReadReplicaPermit(ctx, fact.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	record := store.CredentialRecord{ID: strings.Repeat("e", 32), Kind: "receipt", App: "hello", CredentialRef: "primary", Requester: "fixture", PlanID: "sha256:" + strings.Repeat("f", 64), PlanHash: "sha256:" + strings.Repeat("f", 64), TargetHash: "sha256:" + strings.Repeat("b", 64), PolicyHash: desired.PolicyHash, BindingID: permit.Replica.BindingID, Destination: permit.Replica.Destination.Reference, EpochID: permit.Replica.EpochID, Version: 1, ReceivedAt: time.Now().UTC()}
	if err = state.SaveCredentialRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	services := &preparedServices{}
	manager := &systemd.Fake{DaemonReloadFunc: func(context.Context) error { return nil }, ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		if services.active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}}
	preparation := DataPreparation{State: state, StateRoot: stateRoot, Home: home, Permits: replicapermits.StorePermits{State: state, Configs: replication.DiskConfigs{}}, Publisher: replication.ArtifactPublisher{StateRoot: stateRoot, UnitRoot: filepath.Join(home, ".config/systemd/user")}, Services: services, Units: manager}
	planned := plan.Plan{DataMounts: []data.Mount{{Database: fact.Database, HostPath: filepath.Join(string(fact.Database.Root), fact.Database.RelativeDirectory), ContainerPath: fact.Database.MountPath, BindingID: fact.Database.ReplicaBindingID}}}
	if ready, _ := preparation.PersistentPrepared(ctx, "fixture", planned, desired); ready {
		t.Fatal("unpublished artifacts claimed prepared")
	}
	if err = preparation.PreparePersistent(ctx, "fixture", planned, desired); err != nil {
		t.Fatal(err)
	}
	if services.starts != 1 {
		t.Fatal("replica not activated")
	}
	if ready, err := preparation.PersistentPrepared(ctx, "fixture", planned, desired); err != nil || !ready {
		t.Fatal("complete artifacts not proven", err)
	}
	dbPath := filepath.Join(planned.DataMounts[0].HostPath, "app.db")
	if _, err = os.Lstat(dbPath); !os.IsNotExist(err) {
		t.Fatal("preparation initialized app schema", err)
	}
	name, err := replication.ServiceName(string(fact.Database.ReplicaBindingID))
	if err != nil {
		t.Fatal(err)
	}
	servicePath := filepath.Join(home, ".config/systemd/user", name)
	if err = os.WriteFile(servicePath, []byte("foreign-unit"), 0600); err != nil {
		t.Fatal(err)
	}
	if ready, _ := preparation.PersistentPrepared(ctx, "fixture", planned, desired); ready {
		t.Fatal("wrong installed replica unit accepted")
	}
	if err = preparation.PreparePersistent(ctx, "fixture", planned, desired); err == nil {
		t.Fatal("foreign artifact silently replaced")
	}
	if services.starts != 1 {
		t.Fatal("uncertain preparation restarted replica")
	}
}

func TestPrivateChildRefusesSymlinkAndNeverRepairsModes(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateChild(home, ".config/systemd/user"); err == nil {
		t.Fatal("symlink ancestry accepted")
	}
	if _, err := os.Lstat(filepath.Join(foreign, "systemd")); !os.IsNotExist(err) {
		t.Fatal("foreign descendants created", err)
	}
	if err := os.Remove(filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, ".config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateChild(home, ".config/systemd/user"); err == nil {
		t.Fatal("foreign modes repaired")
	}
	info, err := os.Stat(filepath.Join(home, ".config"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("mode changed", err)
	}
}
