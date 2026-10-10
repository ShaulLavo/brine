//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	// Socket paths include immutable binding IDs. Keep this small state fixture
	// independent of TMPDIR so long worktree paths cannot cross the Unix limit.
	// Use /var/tmp because /tmp may be tmpfs, which private data roots reject.
	stateRoot, err := os.MkdirTemp("/var/tmp", "brine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateRoot) })
	state, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	facts.Store = state
	fact := collectPersistent(t, facts, desired)
	home := t.TempDir()
	if err = os.Chmod(home, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
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
	preparation := DataPreparation{Runner: &preparationPullRunner{}, ProbeRoot: facts.ProbeRoot, ProbeMapping: facts.ProbeMapping, State: state, StateRoot: stateRoot, Home: home, Permits: replicapermits.StorePermits{State: state, Configs: replication.DiskConfigs{}}, Publisher: replication.ArtifactPublisher{StateRoot: stateRoot, UnitRoot: filepath.Join(home, ".config/systemd/user")}, Services: services, Units: manager}
	planned := plan.Plan{DataCredentials: []data.CredentialEvidence{{BindingID: record.BindingID, EpochID: record.EpochID, Destination: record.Destination, Reference: record.CredentialRef, Version: record.Version, PolicyHash: record.PolicyHash, ReceivedAt: record.ReceivedAt}}, DataMounts: []data.Mount{{Database: fact.Database, HostPath: filepath.Join(string(fact.Database.Root), fact.Database.RelativeDirectory), ContainerPath: fact.Database.MountPath, BindingID: fact.Database.ReplicaBindingID}}}
	if ready, _ := preparation.PersistentPrepared(ctx, "fixture", planned, desired); ready {
		t.Fatal("unpublished artifacts claimed prepared")
	}
	for _, change := range []struct {
		name   string
		mutate func(*plan.Plan)
	}{
		{"missing", func(p *plan.Plan) { p.DataCredentials = nil }},
		{"duplicate", func(p *plan.Plan) { p.DataCredentials = append(p.DataCredentials, p.DataCredentials[0]) }},
		{"unapproved version", func(p *plan.Plan) { p.DataCredentials[0].Version = 2 }},
		{"scope drift", func(p *plan.Plan) { p.DataCredentials[0].Reference = "foreign" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := planned
			candidate.DataCredentials = append([]data.CredentialEvidence(nil), planned.DataCredentials...)
			change.mutate(&candidate)
			if err := preparation.PreparePersistent(ctx, "fixture", candidate, desired); err == nil {
				t.Fatal("unapproved credential accepted")
			}
			if services.starts != 0 {
				t.Fatal("refused credential started replica")
			}
		})
	}
	newer := record
	newer.ID = strings.Repeat("d", 32)
	newer.Version = 2
	if err = state.SaveCredentialRecord(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(stateRoot, "credentials/s3/primary/v2.env"), []byte("AWS_ACCESS_KEY_ID=new-access\nAWS_SECRET_ACCESS_KEY=new-secret\n"), 0600); err != nil {
		t.Fatal(err)
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
	committed, err := state.ReadReplicaPermit(ctx, fact.Database.DatabaseID)
	if err != nil || committed.Replica.CredentialVersion != 1 {
		t.Fatal("new delivery substituted after approval", err)
	}
	facts.StateRoot = stateRoot
	refreshed := collectPersistent(t, facts, desired)
	if refreshed.Credentials.Value == nil || refreshed.Credentials.Value.Version != 1 {
		t.Fatal("facts selected newest rather than committed version")
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
	if err := os.Chmod(home, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
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
	if err := os.Mkdir(filepath.Join(home, ".config"), 0755); err != nil { //nolint:gosec // Intentional unsafe-permission fixture proves fail-closed refusal.
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

type enrolledHomeInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i enrolledHomeInfo) Sys() any { return &i.stat }

func TestPrivateChildrenAnchorBelowEnrolledHome(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil { //nolint:gosec // Enrollment-defined ancestor fixture.
		t.Fatal(err)
	} //nolint:gosec // Enrollment-defined ancestor fixture.
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	enrolled := enrolledHomeInfo{FileInfo: info, stat: syscall.Stat_t{Uid: 0, Gid: 1234}}
	base, relative, err := privateChildBase("/home/brine", ".config/systemd/user", enrolled, 1234)
	if err != nil || base != "/home/brine/.config" || relative != "systemd/user" {
		t.Fatal("enrolled home refused", base, relative, err)
	}
	for _, tc := range []struct {
		relative string
		gid      uint32
		uid      uint32
	}{
		{".config/systemd/user", 9999, 0}, {".config/systemd/user", 1234, 1234}, {"new-private-child", 1234, 0}, {".ssh/child", 1234, 0},
	} {
		enrolled.stat.Uid = tc.uid
		if _, _, err := privateChildBase("/home/brine", tc.relative, enrolled, tc.gid); err == nil {
			t.Fatal("foreign or non-enrolled ancestry accepted", tc)
		}
	}
}
