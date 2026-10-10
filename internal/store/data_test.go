//go:build linux

package store

import (
	"context"
	"errors"
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
	binding.ConfigSHA256 = strings.Repeat("b", 64)
	binding.UnitSHA256 = strings.Repeat("c", 64)
	binding.CredentialVersion = 1
	binding.CredentialFile = "/srv/brine-state/credentials/s3/primary/v1.env"
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
