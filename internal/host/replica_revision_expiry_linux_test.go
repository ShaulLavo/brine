//go:build linux

package host

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type expiryReplicaServices struct{ *preparedServices }

func (s expiryReplicaServices) ReplicaStopped(ctx context.Context, name string) (bool, error) {
	if s.active {
		return false, replication.ErrPermit
	}
	return s.preparedServices.ReplicaStopped(ctx, name)
}

func TestReplicaCadenceExpiredCredentialCanRenewAfterSettlement(t *testing.T) {
	for _, boundary := range []string{"stop_unknown", "stop_running", "lifetime_lock", "reload", "start_unknown"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			expiry := time.Now().UTC().Add(time.Hour)
			f := preparationFixtureWithExpiry(t, &expiry)
			f.preparation.Services = expiryReplicaServices{f.services}
			if err := f.preparation.PreparePersistent(ctx, "initial", f.planned, f.desired); err != nil {
				t.Fatal(err)
			}
			before, err := f.preparation.State.ReadReplicaPermit(ctx, f.fact.Database.DatabaseID)
			if err != nil {
				t.Fatal(err)
			}
			f.planned.Lifecycle = plan.ReviseReplica
			f.desired.Databases[0].SyncInterval = 2 * time.Minute
			var unlock func() error
			switch boundary {
			case "stop_unknown", "stop_running":
				f.services.unknownStop = true
			case "lifetime_lock":
				lock, err := replication.AcquireLifetimeLock(ctx, before.Replica.LifetimeLockFile)
				if err != nil {
					t.Fatal(err)
				}
				unlock = lock.Release
			case "reload":
				f.manager.DaemonReloadFunc = func(context.Context) error { return replication.ErrRestartUnknown }
			case "start_unknown":
				f.services.unknownStart = true
			}
			if err = f.preparation.PreparePersistent(ctx, "cadence", f.planned, f.desired); !errors.Is(err, replication.ErrRestartUnknown) {
				t.Fatal("interruption not retained", err)
			}
			pending, err := f.preparation.State.PendingReplicaRevision(ctx, before.Replica.BindingID)
			if err != nil {
				t.Fatal(err)
			}
			if unlock != nil {
				if err = unlock(); err != nil {
					t.Fatal(err)
				}
			}
			f.manager.DaemonReloadFunc = func(context.Context) error { return nil }
			clock := func() time.Time { return expiry.Add(time.Minute) }
			f.preparation.Now = clock
			starts := f.services.starts
			if err = f.preparation.PreparePersistent(ctx, "cadence", f.planned, f.desired); err == nil || f.services.starts != starts {
				t.Fatal("expired cadence restarted", err)
			}
			host := replicaRotation{now: clock, state: f.preparation.State, stateRoot: f.preparation.StateRoot, home: f.preparation.Home, services: f.preparation.Services, units: f.manager, permits: f.preparation.Permits, verifyRemote: func(context.Context, restore.Destination, restore.Credentials) error { return nil }}
			if err = host.Start(ctx, pending.Before); err == nil || f.services.starts != starts {
				t.Fatal("expired credentials issued start", err)
			}
			service := backupcredentials.Service{Requester: "fixture", Files: backupcredentials.Files{Root: filepath.Join(f.preparation.StateRoot, "credentials")}, Journal: credentialJournal{state: f.preparation.State, requester: "fixture"}, Scope: func(context.Context, string) (backupcredentials.Scope, error) {
				return backupcredentials.Scope{TargetHash: f.record.TargetHash, App: "hello", CredentialRef: "primary", Destination: string(f.record.Destination), Binding: string(f.record.BindingID), Epoch: string(f.record.EpochID), PolicyHash: f.desired.PolicyHash}, nil
			}}
			rotation := backupcredentials.Rotator{Journal: rotationJournal{state: f.preparation.State}, Host: host, Now: clock}
			service.Activate = rotation.Activate
			freshPlan, err := service.Plan(ctx, "hello", nil)
			if err != nil {
				t.Fatal(err)
			}
			packet, err := backupcredentials.DecodePacket([]byte(`{"access_key_id":"fixture-new","secret_access_key":"fixture-new-secret"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Clear()
			if boundary == "stop_unknown" {
				f.manager.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return true, nil }
				if _, err = service.Deliver(ctx, freshPlan, packet); err == nil || f.services.starts != starts {
					t.Fatal("unsettled stop permitted renewal", err)
				}
				stillPending, err := f.preparation.State.ReadReplicaRevision(ctx, pending.ID)
				if err != nil || stillPending.Stage == data.RevisionCancelled {
					t.Fatal("unsettled cursor cancelled", err)
				}
				f.manager.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
			}
			// Independent manager settlement, not a repeated uncertain stop/start.
			f.services.active, f.services.unknownStop, f.services.unknownStart = boundary == "stop_running", false, false
			result, err := service.Deliver(ctx, freshPlan, packet)
			if err != nil || !result.Activated || result.Version != 2 {
				t.Fatal("renewal deadlocked", err, result.ActivationStatus)
			}
			cancelled, err := f.preparation.State.ReadReplicaRevision(ctx, pending.ID)
			if err != nil || cancelled.Stage != data.RevisionCancelled || cancelled.ReplacementCredential != 2 {
				t.Fatal("cancellation not durable", err)
			}
			after, err := f.preparation.State.ReadReplicaPermit(ctx, before.Replica.DatabaseID)
			if err != nil || after.Replica.CredentialVersion != 2 || after.Replica.EpochID != before.Replica.EpochID || f.services.starts != starts+1 {
				t.Fatal("wrong renewal effects", err)
			}
		})
	}
}
