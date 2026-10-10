//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type selectedReplicaServices struct {
	preparedServices
	names []string
}

func (s *selectedReplicaServices) Start(ctx context.Context, name string) error {
	s.names = append(s.names, "start "+name)
	return s.preparedServices.Start(ctx, name)
}
func (s *selectedReplicaServices) Stop(ctx context.Context, name string) error {
	s.names = append(s.names, "stop "+name)
	return s.preparedServices.Stop(ctx, name)
}

func TestHostRotationReplacesOnlySelectedReplicaCredential(t *testing.T) {
	ctx := context.Background()
	_, desired, facts := persistentHostFixture(t)
	root := shortReplicaStateRoot(t)
	state, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	facts.Store = state
	fact := collectPersistent(t, facts, desired)
	permit, err := state.ReadReplicaPermit(ctx, fact.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateChild(root, "credentials/s3/primary"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "credentials/s3/primary/v1.env"), []byte("AWS_ACCESS_KEY_ID=fixture-old\nAWS_SECRET_ACCESS_KEY=fixture-old-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldReceipt := store.CredentialRecord{ID: strings.Repeat("e", 32), Kind: "receipt", App: "hello", CredentialRef: "primary", Requester: "fixture", PlanID: "sha256:" + strings.Repeat("f", 64), PlanHash: "sha256:" + strings.Repeat("f", 64), TargetHash: "sha256:" + strings.Repeat("b", 64), PolicyHash: desired.PolicyHash, BindingID: permit.Replica.BindingID, Destination: permit.Replica.Destination.Reference, EpochID: permit.Replica.EpochID, Version: 1, ReceivedAt: time.Now().UTC()}
	if err := state.SaveCredentialRecord(ctx, oldReceipt); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil { //nolint:gosec // Private directory requires traversal permission.
		t.Fatal(err)
	} //nolint:gosec // Private directory requires traversal permission.
	services := &selectedReplicaServices{}
	manager := &systemd.Fake{DaemonReloadFunc: func(context.Context) error { return nil }, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }, ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		if services.active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}}
	permits := replicapermits.StorePermits{State: state, Configs: replication.DiskConfigs{}}
	preparation := DataPreparation{Runner: &preparationPullRunner{}, ProbeRoot: facts.ProbeRoot, ProbeMapping: facts.ProbeMapping, State: state, StateRoot: root, Home: home, Permits: permits, Publisher: replication.ArtifactPublisher{StateRoot: root, UnitRoot: filepath.Join(home, ".config/systemd/user")}, Services: services, Units: manager}
	planned := plan.Plan{DataCredentials: []data.CredentialEvidence{{BindingID: oldReceipt.BindingID, EpochID: oldReceipt.EpochID, Destination: oldReceipt.Destination, Reference: oldReceipt.CredentialRef, Version: oldReceipt.Version, PolicyHash: oldReceipt.PolicyHash, ReceivedAt: oldReceipt.ReceivedAt}}, DataMounts: []data.Mount{{Database: fact.Database, HostPath: filepath.Join(string(fact.Database.Root), fact.Database.RelativeDirectory), ContainerPath: fact.Database.MountPath, BindingID: fact.Database.ReplicaBindingID}}}
	if err := preparation.PreparePersistent(ctx, "fixture", planned, desired); err != nil {
		t.Fatal(err)
	}
	current, err := state.ReadReplicaPermit(ctx, fact.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	before := current.Replica
	config, err := os.Stat(before.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(before.LifetimeLockFile)
	if err != nil {
		t.Fatal(err)
	}
	service := backupcredentials.Service{Requester: "fixture", Files: backupcredentials.Files{Root: filepath.Join(root, "credentials")}, Journal: credentialJournal{state: state, requester: "fixture"}, Scope: func(context.Context, string) (backupcredentials.Scope, error) {
		return backupcredentials.Scope{TargetHash: oldReceipt.TargetHash, App: "hello", CredentialRef: "primary", Destination: string(oldReceipt.Destination), Binding: string(oldReceipt.BindingID), Epoch: string(oldReceipt.EpochID), PolicyHash: desired.PolicyHash}, nil
	}}
	freshPlan, err := service.Plan(ctx, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := backupcredentials.DecodePacket([]byte(`{"access_key_id":"fixture-new","secret_access_key":"fixture-new-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Clear()
	remoteCalls := 0
	host := replicaRotation{state: state, stateRoot: root, home: home, services: services, units: manager, permits: permits, verifyRemote: func(_ context.Context, d restore.Destination, c restore.Credentials) error {
		remoteCalls++
		if d.Prefix != strings.TrimSuffix(before.RemotePrefix, "/") || c.AccessKey != "fixture-new" || c.SecretKey != "fixture-new-secret" {
			t.Error("remote proof not scoped to newly committed credentials")
		}
		return nil
	}}
	rotator := backupcredentials.Rotator{Journal: rotationJournal{state: state}, Host: host}
	service.Activate = rotator.Activate
	// Production delivery holds this same host mutation lock while activating.
	held, err := state.AcquireHostLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, activationErr := service.Deliver(ctx, freshPlan, packet)
	releaseErr := held.Release()
	if activationErr != nil || releaseErr != nil || !result.Activated || remoteCalls != 1 {
		t.Fatal("real composition failed", activationErr, releaseErr)
	}
	after, err := state.ReadReplicaPermit(ctx, fact.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Replica.CredentialVersion != 2 || after.Replica.EpochID != before.EpochID || after.Replica.BindingID != before.BindingID || services.starts != 2 || len(services.names) != 3 {
		t.Fatal("rotation changed identity or wrong lifecycle count")
	}
	name, err := replication.ServiceName(string(before.BindingID))
	if err != nil {
		t.Fatal(err)
	}
	if services.names[1] != "stop "+name || services.names[2] != "start "+name {
		t.Fatal("app or foreign replica affected")
	}
	configAfter, err := os.Stat(before.ConfigFile)
	if err != nil || !os.SameFile(config, configAfter) {
		t.Fatal("config replaced")
	}
	lockAfter, err := os.Stat(before.LifetimeLockFile)
	if err != nil || !os.SameFile(lock, lockAfter) {
		t.Fatal("lifetime lock replaced")
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config/systemd/user", name)) //nolint:gosec // Selected replica name and private test home derive this path.
	if err != nil || !strings.Contains(string(unit), "v2.env") || strings.Contains(string(unit), "v1.env") {
		t.Fatal("unit did not select new immutable credential")
	}
}

func shortReplicaStateRoot(t *testing.T) string {
	t.Helper()
	// A fixed short base keeps unix socket paths under 108 bytes even when
	// TMPDIR points into a deep worktree.
	root, err := os.MkdirTemp("/var/tmp", "brine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
