package sqlitestatus

import (
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/restore"
)

func TestComposeRunningDoesNotMeanDurable(t *testing.T) {
	now := time.Now().UTC()
	facts := Facts{App: "example", Database: "main", DatabaseID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BindingID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EpochID: "cccccccccccccccccccccccccccccccc", Committed: true}
	facts.Credential = Credential{State: "available", Version: 1}
	observations := Observations{Service: Service{State: "active", ObservedAt: now}, Remote: restore.RemoteProbe{State: restore.RemoteNetworkUnavailable, ObservedAt: now}, Watermark: Watermark{State: "failed", ObservedAt: now}}
	status := Compose(now, facts, observations)
	if status.State != "degraded" || status.LocalPosition != nil || status.PositionDistance != nil || status.Loss != "unknown" || status.LastVerified != nil {
		t.Fatalf("untruthful status: %+v", status)
	}
	if !status.Valid() {
		t.Fatal("composed invalid status")
	}
}

func TestComposeCredentialAndLifecycleFacts(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(10 * time.Minute)
	facts := Facts{App: "example", Database: "main", DatabaseID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BindingID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EpochID: "cccccccccccccccccccccccccccccccc", Credential: Credential{State: "available", Version: 1, ExpiresAt: &expiry}}
	facts.Lifecycle = Lifecycle{State: "stale", VerifiedAt: &now}
	status := Compose(now, facts, Observations{Service: Service{State: "unknown", ObservedAt: now}, Remote: restore.RemoteProbe{State: restore.RemoteNotChecked, ObservedAt: now}, Watermark: Watermark{State: "not_checked", ObservedAt: now}})
	if !status.Credential.RenewalNeeded || status.Credential.RemainingSeconds == nil || *status.Credential.RemainingSeconds != 600 || status.State != "degraded" {
		t.Fatalf("missing expiry: %+v", status)
	}
}

func TestComposeAllClosedRemoteFailuresRemainPartial(t *testing.T) {
	for _, remote := range []restore.RemoteState{restore.RemoteEmpty, restore.RemoteAuthRefused, restore.RemoteCredentialsRejected, restore.RemoteDestinationMissing, restore.RemoteStorageUnavailable, restore.RemoteNetworkUnavailable, restore.RemoteResponseInvalid, restore.RemoteNotChecked} {
		now := time.Now().UTC()
		status := Compose(now, Facts{App: "example", Database: "main", DatabaseID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BindingID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EpochID: "cccccccccccccccccccccccccccccccc", Committed: true, Credential: Credential{State: "missing"}}, Observations{Service: Service{State: "active", ObservedAt: now}, Remote: restore.RemoteProbe{State: remote, ObservedAt: now}, Watermark: Watermark{State: "failed", ObservedAt: now}})
		if status.State != "degraded" || !status.Valid() || status.Loss != "unknown" {
			t.Fatalf("untruthful %s result: %+v", remote, status)
		}
	}
}

func TestComposeLifecycleExpiresDuringRemoteProbes(t *testing.T) {
	now := time.Now().UTC()
	at := now.Add(-time.Minute)
	until := now.Add(-time.Second)
	f := Facts{Lifecycle: Lifecycle{State: "fresh", VerifiedAt: &at, FreshUntil: &until}}
	s := Compose(now, f, Observations{})
	if s.Lifecycle.State != "stale" {
		t.Fatal("probe latency preserved expired lifecycle evidence as fresh")
	}
}
