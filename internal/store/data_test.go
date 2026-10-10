//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func dataRequest() DataReservation {
	return DataReservation{App: "example", PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: data.Database{Name: "main", PersistentRoot: "/srv/brine-data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}, Destination: data.Destination{Reference: "primary", Endpoint: "https://storage.example", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}}
}
func TestDataReservationAndPermit(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	req := dataRequest()
	first, err := s.ReserveDatabase(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ReserveDatabase(ctx, req)
	if err != nil || first != second {
		t.Fatalf("reservation identity changed: %v", err)
	}
	if !data.ValidID(string(first.Database.DatabaseID)) || first.Database.IncarnationID == data.AppIncarnationID(first.Replica.EpochID) {
		t.Fatal("invalid independent identities")
	}
	state, err := s.ReadReplicaPermit(ctx, first.Database.DatabaseID)
	if err != nil || state.AllowsReplica(first.Replica.BindingID, first.Replica.EpochID, strings.Repeat("b", 64)) {
		t.Fatalf("uncommitted permit allowed: %v", err)
	}
	binding := first.Replica
	binding.ConfigContent = "dbs: []\n"
	sum := sha256.Sum256([]byte(binding.ConfigContent))
	binding.ConfigSHA256 = hex.EncodeToString(sum[:])
	binding.ConfigFile = filepath.Join(s.dir, "replication", string(binding.BindingID), "litestream.yml")
	binding.SocketFile = filepath.Join(s.dir, "replication", string(binding.BindingID), "control.sock")
	binding.LifetimeLockFile = filepath.Join(s.dir, "replica-locks", string(binding.BindingID)+".lock")
	binding.UnitSHA256 = strings.Repeat("c", 64)
	binding.CredentialVersion = 1
	binding.CredentialFile = filepath.Join(s.dir, "credentials/s3/primary/v1.env")
	if err = s.CommitReplicaBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	state, err = s.ReadReplicaPermit(ctx, first.Database.DatabaseID)
	if err != nil || !state.AllowsReplica(binding.BindingID, binding.EpochID, binding.ConfigSHA256) {
		t.Fatalf("committed permit blocked: %v", err)
	}
	if state.AllowsReplica(binding.BindingID, binding.EpochID, strings.Repeat("d", 64)) {
		t.Fatal("wrong config allowed")
	}
	fence, err := s.HoldDataFence(ctx, first.Database.DatabaseID, "operation-1")
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.ReadReplicaPermit(ctx, first.Database.DatabaseID)
	if err != nil || state.AllowsReplica(binding.BindingID, binding.EpochID, binding.ConfigSHA256) {
		t.Fatalf("fenced permit allowed: %v", err)
	}
	if err = s.ReleaseDataFence(ctx, fence.ID, "wrong-operation"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong owner release: %v", err)
	}
	if err = s.ReleaseDataFence(ctx, fence.ID, "operation-1"); err != nil {
		t.Fatal(err)
	}
	scopes, err := s.ReadCredentialScopes(ctx, "example")
	if err != nil || len(scopes) != 1 || scopes[0].FenceHeld || scopes[0].PolicyHash != req.PolicyHash {
		t.Fatalf("scope mismatch: %v", err)
	}
	read, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	writer, err := read.ReadWriterPermits(ctx, first.Database.IncarnationID)
	if err != nil || len(writer) != 1 || writer[0].DBPath == "" || writer[0].Replica.ConfigContent != binding.ConfigContent {
		t.Fatalf("read-only writer permits: %v", err)
	}
	byBinding, err := read.ReadReplicaPermitByBinding(ctx, binding.BindingID)
	if err != nil || byBinding.CredentialPath != binding.CredentialFile {
		t.Fatalf("binding projection: %v", err)
	}
	changed := req
	changed.Database.Filename = "other.db"
	if _, err = s.ReserveDatabase(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed layout accepted: %v", err)
	}
	binding.RemotePrefix = "other/"
	if err = s.CommitReplicaBinding(ctx, binding); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed prefix accepted: %v", err)
	}
}
func TestDestinationOwnership(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	first, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CheckReplicaDestinationOwner(ctx, first.Replica.Destination.Endpoint, first.Replica.Destination.Bucket, first.Replica.RemotePrefix, first.Replica.BindingID); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckReplicaDestinationOwner(ctx, first.Replica.Destination.Endpoint, first.Replica.Destination.Bucket, first.Replica.RemotePrefix, data.ReplicaBindingID(strings.Repeat("f", 32))); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign owner accepted: %v", err)
	}
	if _, err = s.ReadReplicaPermit(ctx, data.DatabaseID(strings.Repeat("e", 32))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing permit: %v", err)
	}
}

func TestCredentialReferenceRecord(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	r := CredentialRecord{ID: id, Kind: "plan", PlanID: "plan-1", Requester: "authenticated-agent", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetHash: "sha256:" + strings.Repeat("b", 64), PolicyHash: dataRequest().PolicyHash, BindingID: reserved.Replica.BindingID, Destination: reserved.Replica.Destination.Reference, EpochID: reserved.Replica.EpochID}
	if err = s.SaveCredentialRecord(ctx, r); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadCredentialRecord(ctx, id)
	if err != nil || loaded.Requester != r.Requester || loaded.EpochID != r.EpochID {
		t.Fatalf("credential reference record: %v", err)
	}
	r.Requester = "other"
	if err = s.SaveCredentialRecord(ctx, r); err == nil {
		t.Fatal("immutable credential record replaced")
	}
	r.ID, _ = data.NewID()
	r.Requester = ""
	if err = s.SaveCredentialRecord(ctx, r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unauthenticated requester: %v", err)
	}
	for _, table := range []string{"data_incarnations", "data_databases", "data_replica_bindings", "data_credential_records"} {
		if _, err = s.db.ExecContext(ctx, "DELETE FROM "+table); err == nil {
			t.Fatalf("immutable records deleted from %s", table)
		}
	}
}
